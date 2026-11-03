package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"nixmox/internal/api"
	"nixmox/internal/cluster"
	"nixmox/internal/discovery"
	"nixmox/internal/domain"
	"nixmox/internal/host"
)

// startPending restores an enrolled installer without creating servers, seeding
// shared state, or reading full-member signing secrets. The contact has already
// committed membership before sending the certificate.
func (r *Runtime) startPending(saved startup) error {
	ctx, cancel := context.WithTimeout(r.lifetime, 30*time.Second)
	defer cancel()
	if err := r.node.VerifyEnrollment([]byte(saved.Certificate), []byte(saved.Cluster.CA.Certificate), time.Now()); err != nil {
		return err
	}
	key, err := os.ReadFile(filepath.Join(r.config.StateDir, "node.key"))
	if err != nil {
		return err
	}
	defer clear(key)
	tlsConfig, err := cluster.TLSConfig([]byte(saved.Certificate), key, []byte(saved.Cluster.CA.Certificate))
	if err != nil {
		return err
	}
	nc, err := cluster.Connect(saved.NATSEndpoints, tlsConfig, r.node.State.NodeID)
	if err != nil {
		return err
	}
	success := false
	defer func() {
		if !success {
			nc.Close()
		}
	}()
	js, err := jetstream.New(nc)
	if err != nil {
		return err
	}
	members, err := cluster.OpenStore[domain.Member](ctx, js, "MEMBERS")
	if err != nil {
		return err
	}
	current, err := members.Get(ctx, r.node.State.NodeID)
	if err != nil {
		return err
	}
	if current.Value.ClusterID != saved.Cluster.ID || current.Value.Status != "pending" {
		return errors.New("pending startup differs from authoritative membership")
	}
	clusters, err := cluster.OpenStore[domain.Cluster](ctx, js, "CLUSTER")
	if err != nil {
		return err
	}
	discoveries, err := cluster.OpenStore[discovery.Record](ctx, js, "DISCOVERY")
	if err != nil {
		return err
	}
	metrics := cluster.Metrics{Connection: nc}
	svc := &api.Services{Pending: true, Cluster: clusters, Members: members, Discovery: discoveries, Metrics: metrics}
	svc.HostDisks = func(ctx context.Context, nodeID string) (any, error) {
		if nodeID != r.node.State.NodeID {
			return nil, errors.New("pending member can only inspect its own disks")
		}
		return (host.Process{}).Disks(ctx)
	}
	r.mu.Lock()
	r.node.State.ClusterID = saved.Cluster.ID
	r.node.State.Status = "pending"
	err = r.node.Save()
	if err == nil {
		r.client, r.services, r.status = nc, svc, "online"
	}
	r.mu.Unlock()
	if err != nil {
		return err
	}
	if err = r.startObservations(nc, r.node.State.NodeID); err != nil {
		return err
	}
	success = true
	r.workers.Go(func() { host.Metrics(r.lifetime, r.node.State.NodeID, metrics) })
	return nil
}
