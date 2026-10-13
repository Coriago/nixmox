package cluster

import (
	"fmt"

	"github.com/nats-io/nats-server/v2/server"
)

// Diagnostics is a local snapshot, available even when Raft has no quorum.
// It deliberately does not turn liveness or a cached leader into a write-health claim.
type Diagnostics struct {
	Mode    string                  `json:"mode"`
	Meta    *server.MetaClusterInfo `json:"meta,omitempty"`
	Streams []server.StreamDetail   `json:"streams"`
}

func (s *Supervisor) Diagnostics() (Diagnostics, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := Diagnostics{Mode: "standalone", Streams: []server.StreamDetail{}}
	if s.inClusterMode {
		out.Mode = "raft"
	}
	if s.emb == nil {
		return out, fmt.Errorf("embedded server unavailable")
	}
	info, err := s.emb.Srv.Jsz(&server.JSzOptions{Accounts: true, Streams: true, Config: true, RaftGroups: true})
	if err != nil {
		return out, err
	}
	out.Meta = info.Meta
	for _, account := range info.AccountDetails {
		for _, stream := range account.Streams {
			if stream.Name == "KV_hosts" || stream.Name == "KV_applylog" {
				out.Streams = append(out.Streams, stream)
			}
		}
	}
	return out, nil
}
