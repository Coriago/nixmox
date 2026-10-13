package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"nixmox/internal/cluster"
	"nixmox/internal/portal"
)

// ClusterAPI is the server's seam to the cluster supervisor.
type ClusterAPI interface {
	State(memberCount int) cluster.ClusterState
	CreateCluster(ctx context.Context, name string) error
	JoinCluster(ctx context.Context, seedHTTPAddr string) error
	AcceptJoin(req cluster.JoinRequest) (cluster.JoinResponse, error)
}

func (s *Server) MountCluster(p *portal.Portal) {
	s.Mux.Get("/api/v1/cluster/diagnostics", s.clusterDiagnostics)
	s.Mux.Post("/api/v1/cluster/add", s.addDiscoveredNode)
	s.Mux.Post("/api/v1/cluster/create", func(w http.ResponseWriter, r *http.Request) {
		if s.Cluster == nil {
			http.Error(w, "clustering unavailable", http.StatusServiceUnavailable)
			return
		}
		name := r.FormValue("name")
		if name == "" {
			var req struct {
				Name string `json:"name"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err == nil {
				name = req.Name
			}
		}
		if err := s.Cluster.CreateCluster(r.Context(), name); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.writeClusterState(w)
	})

	// Peer-side join: other nodes call this during their join. The portal's
	// own "Join" button uses /join/initiate below — never this one directly.
	s.Mux.Post("/api/v1/cluster/join", func(w http.ResponseWriter, r *http.Request) {
		if s.Cluster == nil {
			http.Error(w, "clustering unavailable", http.StatusServiceUnavailable)
			return
		}
		var req cluster.JoinRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		resp, err := s.Cluster.AcceptJoin(req)
		if errors.Is(err, cluster.ErrClusterNameMismatch) {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	s.Mux.Post("/api/v1/cluster/join/initiate", func(w http.ResponseWriter, r *http.Request) {
		if s.Cluster == nil {
			http.Error(w, "clustering unavailable", http.StatusServiceUnavailable)
			return
		}
		seed := r.FormValue("seed")
		if seed == "" {
			var req struct {
				Seed string `json:"seed"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err == nil {
				seed = req.Seed
			}
		}
		if seed == "" {
			http.Error(w, "seed is required", http.StatusBadRequest)
			return
		}
		if err := s.Cluster.JoinCluster(r.Context(), seed); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		s.writeClusterState(w)
	})
}

func (s *Server) writeClusterState(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	count := 0
	if s.View != nil {
		count = s.View.Count()
	}
	json.NewEncoder(w).Encode(s.Cluster.State(count))
}
