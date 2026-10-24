package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"nixmox/internal/domain"
	"nixmox/internal/engine/comms"
	"nixmox/internal/logging"
)

func storageLimits(existing bool) comms.StorageLimits {
	return comms.StorageLimits{Commands: 16 << 20, State: 8 << 20, Objects: 8 << 20, Replicas: 1, Existing: existing}
}

// applyTopology runs under topologyMu, but does not hold the membership mutex
// while waiting for a peer election. HTTP can distribute the signed staged plan
// before either side has a JetStream leader. Admission stays fenced meanwhile.
func (c *ClusterNode) applyTopology(ctx context.Context) error {
	c.mu.Lock()
	trust := c.keys.Trust
	if trust == nil || trust.Topology == nil || (trust.BrokerTransition == "" && (trust.BrokerTopology == nil || trust.BrokerTopology.Topology.Version == trust.Topology.Topology.Version)) {
		c.mu.Unlock()
		return nil
	}
	c.transitioning = true
	source := c.transport
	next := trust.Topology
	options, err := c.transportOptions(next)
	if err != nil {
		c.mu.Unlock()
		return err
	}
	creator := c.creator()
	destination := comms.StandaloneStore
	if options.RouteAddress != "" {
		destination = comms.RoutedStore
	}
	handoff := creator && (trust.BrokerTransition != "" || source.StoreMode() != destination)
	directory := trust.BrokerTransition
	if handoff && directory == "" {
		directory = filepath.Join(c.config.DataDir, "broker-handoffs", fmt.Sprintf("%d-%d", trust.Issuance.MembershipEpoch, next.Topology.Version))
		copy := *trust
		copy.BrokerTransition = directory
		err = c.keys.Install(copy)
	}
	c.mu.Unlock()
	if err != nil {
		return err
	}
	defer func() { c.mu.Lock(); c.transitioning = false; c.mu.Unlock() }()
	if handoff {
		return c.handoff(ctx, source, options, directory)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	// Routed peers keep their existing store/Raft state when changing routes.
	if source.StoreMode() == comms.RoutedStore && destination == comms.RoutedStore {
		if err = source.Broker.ReloadRoutes(options); err != nil {
			return err
		}
	} else if source.StoreMode() != destination || source.IsLeaf() != options.Pending {
		if destination == comms.RoutedStore {
			options.DataDir = filepath.Join(c.config.DataDir, "broker-stores", fmt.Sprintf("%d-%d", trust.Issuance.MembershipEpoch, next.Topology.Version))
		} else if !creator {
			options.DataDir = filepath.Join(c.config.DataDir, "broker")
		}
		source.Close()
		transport, err := comms.StartRuntime(c.life, options, c.keys.ClientTLS())
		if err != nil {
			return err
		}
		c.transport = transport
		c.notifyTransport()
		if creator {
			store, err := transport.ConnectStorage(ctx, storageLimits(true))
			if err != nil {
				return err
			}
			transport.Store = store
		}
	}
	copy := *c.keys.Trust
	copy.BrokerTopology = next
	copy.BrokerDirectory = options.DataDir
	if err = c.keys.Install(copy); err != nil {
		return err
	}
	c.notifyTransport() // Membership can change handler eligibility without replacing Core.
	if source == c.transport {
		return nil
	}
	return c.observePresence()
}
func (c *ClusterNode) handoff(ctx context.Context, source *comms.Runtime, options comms.BrokerOptions, directory string) error {
	transition, err := comms.OpenStoreTransition(directory)
	if errors.Is(err, os.ErrNotExist) || (err == nil && transition.Status().Phase == comms.TransitionPreparing) {
		id := domain.ID(filepath.Base(directory))
		frozen, e := source.Quiesce(ctx, id)
		if e != nil {
			return e
		}
		c.mu.Lock()
		c.transport = frozen
		c.notifyTransport()
		c.mu.Unlock()
		source = frozen
		destination := comms.StandaloneStore
		if options.RouteAddress != "" {
			destination = comms.RoutedStore
		}
		transition, err = comms.PrepareStoreTransition(ctx, source, directory, id, destination)
	}
	if err != nil {
		return err
	}
	status := transition.Status()
	target := source
	if source.StoreDirectory() != status.TargetDirectory {
		source.Close()
		target, err = transition.StartTarget(c.life, options, c.keys.ClientTLS())
		if err != nil {
			return err
		}
		c.mu.Lock()
		c.transport = target
		c.notifyTransport()
		c.mu.Unlock()
	}
	logging.From(ctx).Info("restoring broker store", "from", string(status.From), "to", string(status.To), "phase", string(status.Phase))
	if status.Phase != comms.TransitionCommitted {
		if err = transition.Restore(ctx, target); err != nil {
			return err
		}
	}
	target, err = transition.Activate(ctx, target)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.transport = target
	c.notifyTransport()
	c.mu.Unlock()
	store, err := target.ConnectStorage(ctx, storageLimits(true))
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	target.Store = store
	c.notifyTransport()
	copy := *c.keys.Trust
	copy.BrokerTopology = copy.Topology
	copy.BrokerDirectory = status.TargetDirectory
	copy.BrokerTransition = ""
	if err = c.keys.Install(copy); err != nil {
		return err
	}
	c.transitioning = false
	if err = c.loadMembership(ctx); err != nil {
		return err
	}
	return c.observePresence()
}
func (c *ClusterNode) observePresence() error {
	if !c.creator() {
		return nil
	}
	core := c.transport.Core
	return core.ObservePresence(func(h domain.Heartbeat) { c.acceptHeartbeat(core, h) }, func(s domain.HostTelemetry) { c.acceptSample(core, s) })
}
