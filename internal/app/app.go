// Package app is the composition root; engines never depend on it.
package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"nixmox/internal/domain"
	"nixmox/internal/engine/identity"
	"nixmox/internal/engine/platform"
	"nixmox/internal/engine/task"
	"nixmox/internal/engine/telemetry"
	"nixmox/internal/gateway"
	"nixmox/internal/runtime"
	"nixmox/internal/service"
)

func Serve(ctx context.Context, config runtime.Config) error {
	life, cancel := context.WithCancel(ctx)
	defer cancel()
	ctx = life
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := config.Validate(); err != nil {
		return err
	}
	addresses, err := config.HTTPAddresses()
	if err != nil {
		return err
	}
	listeners := make([]net.Listener, 0, len(addresses))
	defer func() {
		for _, listener := range listeners {
			listener.Close()
		}
	}()
	for _, address := range addresses {
		listener, err := net.Listen("tcp", address)
		if err != nil {
			return fmt.Errorf("http.port listener %s: %w", address, err)
		}
		listeners = append(listeners, listener)
	}
	journal, err := task.OpenJournal(config.DataDir)
	if err != nil {
		return err
	}
	defer journal.Shutdown()
	keys, err := identity.Open(ctx, config.DataDir)
	if err != nil {
		return err
	}
	id := keys.Identity
	name, err := os.Hostname()
	if err != nil {
		return err
	}
	sampler, err := telemetry.NewSampler(id.NodeID, nil, time.Duration(config.Telemetry.Host.FastIntervalMS)*time.Millisecond, time.Duration(config.Telemetry.Host.FilesystemIntervalMS)*time.Millisecond)
	if err != nil {
		return err
	}
	if config.NodeName != "" {
		name = config.NodeName
	}
	otelConfig := telemetry.OTelConfig{NodeID: id.NodeID, NodeName: name, Logs: os.Stdout,
		MetricsInterval: time.Duration(config.Telemetry.OTLP.MetricsIntervalMS) * time.Millisecond}
	if id.ClusterID != nil {
		otelConfig.ClusterID = *id.ClusterID
	}
	if config.Telemetry.OTLP.Enable {
		otelConfig.Endpoint = *config.Telemetry.OTLP.Endpoint
	}
	otel, err := telemetry.NewOTel(ctx, otelConfig)
	if err != nil {
		return err
	}
	defer otel.Shutdown(context.Background())
	if err = otel.Install(); err != nil {
		return err
	}
	registration, err := otel.ObserveHost(sampler)
	if err != nil {
		return err
	}
	defer registration.Unregister()
	s := service.New()
	s.Identity, s.Journal, s.Sampler = id, journal, sampler
	s.Network = func(ctx context.Context) (domain.NetworkStatus, error) {
		return platform.NetworkStatus(ctx, id.NodeID, config.Network.ManagementInterfaces)
	}
	s.Host = domain.Host{Record: domain.Record{SchemaVersion: 1, ID: id.NodeID, Version: id.Version},
		Capabilities: domain.Capabilities{Power: config.Capabilities.Power}, Name: name, Membership: domain.Unjoined, Environment: domain.Bootstrap,
		Reachability: domain.Online, BrokerRole: "standalone", Health: domain.Health{Status: "unknown", Reasons: []domain.HealthReason{}},
		PendingReasons: []string{}, Connectivity: []domain.Connectivity{}, ActiveTaskIDs: []domain.ID{}}
	for _, address := range addresses {
		s.Host.Endpoints = append(s.Host.Endpoints, domain.Endpoint{Service: "api", Transport: "http", Address: address, Verified: true})
	}
	s.Cluster, err = service.OpenCluster(ctx, config, keys, s.Host)
	if err != nil {
		return err
	}
	defer s.Cluster.Close()
	s.Cluster.ObserveMembership(otel.SetCluster)
	if config.Discovery.Enable {
		s.Discovery, err = platform.OpenDiscovery(config.Discovery.Port, config.Network.ManagementInterfaces, s.DiscoveryRecords)
		if err != nil {
			return err
		}
		defer s.Discovery.Close()
	}
	if err = s.StartNode(ctx); err != nil {
		return err
	}

	var workers sync.WaitGroup
	results := make(chan error, len(listeners)+11)
	start := func(run func() error) {
		workers.Go(func() { results <- run() })
	}
	server := &http.Server{Handler: gateway.New(s), ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 30 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10,
		BaseContext: func(net.Listener) context.Context { return life }}
	// HTTP stops before workers and persistence. Stream contexts are cancelled so
	// slow or disconnected subscribers cannot keep shutdown open indefinitely.
	defer func() {
		cancel()
		shutdown, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if server.Shutdown(shutdown) != nil {
			server.Close()
		}
		workers.Wait()
	}()
	start(func() error { return sampler.Run(life) })
	start(func() error { return s.Scheduler.Run(life) })
	start(func() error { return s.Cluster.Run(life, sampler) })
	start(func() error { return s.Deliver(life) })
	start(func() error { return s.ServeQueries(life, false) })
	start(func() error { return s.ServeQueries(life, true) })
	start(func() error { return s.ReconcileTasks(life) })
	start(func() error { return s.Cluster.ReconcileMembership(life) })
	start(func() error { return s.Cluster.ReconcileTopology(life) })
	if s.Discovery != nil {
		start(func() error { return s.Discovery.Run(life) })
	}
	if config.Cluster.Name != nil {
		start(func() error { return s.Autojoin(life) })
	}
	for _, listener := range listeners {
		start(func() error { return server.Serve(listener) })
	}
	select {
	case <-ctx.Done():
		return nil
	case err := <-results:
		if errors.Is(err, context.Canceled) || errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
