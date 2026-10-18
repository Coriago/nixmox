package server

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"nixmox/internal/cluster"
	"nixmox/internal/jobs"
)

type Rollout struct {
	State     string   `json:"state"`
	Strategy  string   `json:"strategy"`
	Hosts     []string `json:"hosts"`
	Completed []string `json:"completed"`
	Error     string   `json:"error,omitempty"`
}

func (s *Server) startRollout(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Strategy  string `json:"strategy"`
		BatchSize int    `json:"batch_size"`
	}
	if err := decode(r, &req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if req.Strategy == "" {
		req.Strategy = "serial"
	}
	switch req.Strategy {
	case "serial", "canary":
		req.BatchSize = 1
	case "batch":
		if req.BatchSize < 1 || req.BatchSize > 8 {
			http.Error(w, "batch_size must be 1..8", 400)
			return
		}
	default:
		http.Error(w, "strategy must be serial, canary or batch", 400)
		return
	}
	if s.Apply == nil {
		http.Error(w, "apply unavailable", 503)
		return
	}
	hosts := []cluster.HostPayload{}
	if s.View != nil && s.clusterState().Status == "clustered" {
		hosts = s.View.List()
	}
	if len(hosts) == 0 {
		hosts = append(hosts, cluster.HostPayload{NodeID: s.Config.Hostname, Online: true})
	}
	ids := []string{}
	for _, h := range hosts {
		if !h.Online {
			http.Error(w, "offline host blocks rollout: "+h.NodeID, 409)
			return
		}
		ids = append(ids, h.NodeID)
	}
	s.rolloutMu.Lock()
	if s.rollout.State == "running" {
		s.rolloutMu.Unlock()
		http.Error(w, "rollout already running on this coordinator", 409)
		return
	}
	s.rollout = Rollout{State: "running", Strategy: req.Strategy, Hosts: ids, Completed: []string{}}
	respond(w, 202, s.rollout)
	s.rolloutMu.Unlock()
	s.workers.Add(1)
	go func() { defer s.workers.Done(); s.runRollout(hosts, req.Strategy, req.BatchSize) }()
}
func (s *Server) runRollout(hosts []cluster.HostPayload, strategy string, size int) {
	ctx, cancel := context.WithTimeout(s.lifetime, 2*time.Hour)
	defer cancel()
	var failure error
	for start := 0; start < len(hosts); {
		n := size
		if strategy == "canary" && start == 0 {
			n = 1
		}
		end := start + n
		if end > len(hosts) {
			end = len(hosts)
		}
		var wg sync.WaitGroup
		errors := make(chan error, end-start)
		for _, h := range hosts[start:end] {
			wg.Add(1)
			go func(h cluster.HostPayload) {
				defer wg.Done()
				if err := s.applyAndWait(ctx, h); err != nil {
					errors <- fmt.Errorf("%s: %w", h.NodeID, err)
					return
				}
				s.rolloutMu.Lock()
				s.rollout.Completed = append(s.rollout.Completed, h.NodeID)
				s.rolloutMu.Unlock()
			}(h)
		}
		wg.Wait()
		close(errors)
		for err := range errors {
			if failure == nil {
				failure = err
			}
		}
		if failure != nil {
			break
		}
		start = end
		if strategy == "canary" && start == 1 && start < len(hosts) {
			select {
			case <-ctx.Done():
				failure = ctx.Err()
			case <-time.After(30 * time.Second):
			}
			if failure != nil {
				break
			}
		}
	}
	s.rolloutMu.Lock()
	defer s.rolloutMu.Unlock()
	s.rollout.State = "succeeded"
	if failure != nil {
		s.rollout.State = "failed"
		s.rollout.Error = failure.Error()
	}
}
func (s *Server) applyAndWait(ctx context.Context, h cluster.HostPayload) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var id string
	if s.Bus == nil && h.NodeID == s.Config.Hostname {
		j, err := s.Apply.Apply(ctx)
		if err != nil {
			return err
		}
		id = j.ID
	} else {
		var result struct {
			ID string `json:"id"`
		}
		if err := s.requestPeer(ctx, h, "POST", "/api/v1/apply", nil, &result); err != nil {
			return err
		}
		id = result.ID
	}
	if id == "" {
		return fmt.Errorf("empty job ID")
	}
	tick := time.NewTicker(10 * time.Second)
	defer tick.Stop()
	for {
		var changed <-chan struct{}
		if s.Updates != nil {
			changed = s.Updates.Watch()
		}
		var j jobs.Job
		var err error
		if s.Bus == nil && h.NodeID == s.Config.Hostname {
			j, err = s.Store.Get(id)
		} else {
			err = s.requestPeer(ctx, h, "GET", "/api/v1/jobs/"+id, nil, &j)
		}
		if err != nil {
			return err
		}
		switch j.State {
		case "applied":
			return nil
		case "failed", "cancelled":
			return fmt.Errorf("job %s failed", id)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		case <-changed:
		}
	}
}
