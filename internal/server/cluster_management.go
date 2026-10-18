package server

import (
	"net/http"

	"nixmox/internal/cluster"
)

func (s *Server) clusterDiagnostics(w http.ResponseWriter, r *http.Request) {
	provider, ok := s.Cluster.(interface {
		Diagnostics() (cluster.Diagnostics, error)
	})
	if !ok {
		http.Error(w, "cluster diagnostics unavailable", 503)
		return
	}
	result, err := provider.Diagnostics()
	if err != nil {
		http.Error(w, err.Error(), 503)
		return
	}
	respond(w, 200, result)
}

// Adding a discovery hint requires an explicit selection and confirmation.
// The selected node still executes the normal join handshake itself.
func (s *Server) addDiscoveredNode(w http.ResponseWriter, r *http.Request) {
	if s.clusterState().Status != "clustered" {
		http.Error(w, "create or join a cluster first", 409)
		return
	}
	var req struct {
		NodeID  string `json:"node_id"`
		Address string `json:"address"`
		Confirm string `json:"confirm"`
	}
	if err := decode(r, &req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if req.NodeID == "" || req.NodeID == s.Config.Hostname || req.Confirm != req.NodeID {
		http.Error(w, "confirm must match the discovered node", 400)
		return
	}
	if s.Discoveries != nil {
		for _, peer := range s.Discoveries.List() {
			if peer.NodeID != req.NodeID || peer.Address != req.Address || peer.Cluster != "" || peer.Installer {
				continue
			}
			var result cluster.ClusterState
			err := s.requestPeer(r.Context(), cluster.HostPayload{HTTPAddress: peer.Address}, "POST",
				"/api/v1/cluster/join/initiate", map[string]string{"seed": s.Config.PeerAddress()}, &result)
			if err != nil {
				http.Error(w, err.Error(), 502)
				return
			}
			respond(w, 200, result)
			return
		}
	}
	http.Error(w, "unjoined discovery hint expired or does not match", 409)
}
