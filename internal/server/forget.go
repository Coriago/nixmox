package server

import (
	"context"
	"net/http"
)

func (s *Server) forgetHost(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NodeID string `json:"node_id"`
		Route  string `json:"route"`
	}
	if err := decode(r, &req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if req.NodeID == s.Config.Hostname || req.NodeID == "" {
		http.Error(w, "invalid removed host", 400)
		return
	}
	if peer, ok := s.peer(req.NodeID); ok {
		req.Route = peer.ClusterRoute
	}
	if req.Route == "" {
		http.Error(w, "removed route required", 400)
		return
	}
	supervisor, ok := s.Cluster.(interface {
		ForgetHost(context.Context, string, string, ...string) error
	})
	if !ok {
		http.Error(w, "forget unavailable", 503)
		return
	}
	known := []string{}
	for _, h := range s.View.List() {
		if h.ClusterRoute != "" {
			known = append(known, h.ClusterRoute)
		}
	}
	if err := supervisor.ForgetHost(r.Context(), req.NodeID, req.Route, known...); err != nil {
		http.Error(w, err.Error(), 409)
		return
	}
	s.View.Remove(req.NodeID)
	respond(w, 200, map[string]string{"status": "removed"})
}
