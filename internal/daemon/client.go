package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"nixmox/internal/cache"
	"nixmox/internal/cluster"
	"nixmox/internal/domain"
)

// startClient restores an enrolled member without creating servers, seeding
// shared state, or reading full-member signing secrets. The contact has already
// committed membership before sending the certificate.
func (r *Runtime) startClient(saved startup) error {
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
	if current.Value.ClusterID != saved.Cluster.ID || (current.Value.Status != "pending" && current.Value.Status != "full" && !(current.Value.Status == "full-stable" && len(saved.PeerRoutes) > 0)) {
		return errors.New("pending startup differs from authoritative membership")
	}
	saved.Member = current.Value
	saved.Member.Environment = r.config.Environment
	if len(saved.PeerRoutes) > 0 {
		if saved.Member.Status == "pending" {
			return errors.New("pending member cannot restore a server")
		}
		peer, err := r.startPeer(ctx, saved, members)
		if err != nil {
			return err
		}
		endpoint, err := localClientURL(peer)
		if err != nil {
			return err
		}
		local, err := cluster.Connect([]string{endpoint}, tlsConfig, r.node.State.NodeID)
		if err != nil {
			return err
		}
		nc.Close()
		nc = local
		js, err = jetstream.New(nc)
		if err != nil {
			return err
		}
	}
	svc, err := r.wireServices(ctx, js, nc, saved.Member)
	if err != nil {
		return err
	}
	gateway := svc.Cache.(*cache.Gateway)
	defer func() {
		if !success {
			gateway.Close()
		}
	}()
	if err = r.saveStartup(saved); err != nil {
		return err
	}
	r.mu.Lock()
	r.node.State.ClusterID = saved.Cluster.ID
	r.node.State.Status = saved.Member.Status
	err = r.node.Save()
	if err == nil {
		r.client, r.services, r.status = nc, svc, "online"
		r.gateway = gateway
	}
	r.mu.Unlock()
	if err != nil {
		return err
	}
	if err = r.startObservations(nc, r.node.State.NodeID); err != nil {
		return err
	}
	success = true
	return r.runServices(svc)
}
