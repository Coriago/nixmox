package cluster

import (
	"context"
	"fmt"
	"nixmox/internal/natsx"
	"os"
	"path/filepath"
	"time"
)

// Leave removes this server from the Raft group before stopping its routes.
// Local job/apply history survives; old cluster storage is archived, not reused.
func (s *Supervisor) Leave(ctx context.Context) error {
	s.transition.Lock()
	defer s.transition.Unlock()
	s.mu.RLock()
	emb := s.emb
	clustered := s.inClusterMode
	id := s.ident.NodeID
	hook := s.OnReconfigure
	s.mu.RUnlock()
	if hook != nil {
		hook(ctx, nil)
	}
	if clustered && emb != nil {
		timeout, cancel := context.WithTimeout(ctx, 20*time.Second)
		err := emb.RemovePeer(timeout, id)
		cancel()
		if err != nil {
			if hook != nil {
				hook(ctx, emb)
			}
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.generationCancel != nil {
		s.generationCancel()
	}
	if s.emb != nil {
		s.emb.Stop()
		s.emb = nil
	}
	old := filepath.Join(s.dataDir, "nats-clustered")
	if _, err := os.Stat(old); err == nil {
		if err = os.Rename(old, old+fmt.Sprintf(".left-%d", time.Now().UnixNano())); err != nil {
			return err
		}
	}
	next := nodeIdentity{NodeID: s.cfg.Hostname, Hostname: s.cfg.Hostname, Left: true}
	if err := saveIdentity(s.dataDir, next); err != nil {
		return err
	}
	s.ident = next
	return s.startLocked(natsx.ClusterSpec{})
}

// ForgetRoute persists route removal on surviving nodes. The last survivor
// returns to standalone JetStream while retaining its cluster name.
func (s *Supervisor) ForgetRoute(ctx context.Context, route string, known ...string) error {
	s.transition.Lock()
	defer s.transition.Unlock()
	s.mu.Lock()
	routes := []string{}
	self := fmt.Sprintf("nats://%s:%d", s.clusterIP, s.cfg.ClusterPort)
	for _, r := range dedupe(append(append([]string(nil), s.ident.Routes...), known...)) {
		if r != route && r != self {
			routes = append(routes, r)
		}
	}
	next := s.ident
	next.Routes = routes
	if err := saveIdentity(s.dataDir, next); err != nil {
		s.mu.Unlock()
		return err
	}
	s.ident = next
	if len(routes) > 0 && s.emb != nil {
		err := s.emb.ReloadRoutes(routes)
		s.mu.Unlock()
		return err
	}
	s.mu.Unlock()
	return s.Reconfigure(ctx, natsx.ClusterSpec{})
}
func (v *View) Remove(id string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	delete(v.hosts, id)
	delete(v.seen, id)
}
func (v *View) Clear() {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.hosts = map[string]HostPayload{}
	v.seen = map[string]time.Time{}
}
