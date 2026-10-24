package service

import (
	"context"
	"encoding/json"
	"testing"

	"nixmox/internal/domain"
)

// No direct-execution fallback exists when the NATS ingress is absent.
func TestSubmitRequiresNATS(t *testing.T) {
	s := New()
	s.Identity.NodeID = "node"
	s.Network = func(context.Context) (domain.NetworkStatus, error) {
		t.Fatal("executed outside NATS")
		return domain.NetworkStatus{}, nil
	}
	_, err := s.Submit(context.Background(), domain.Command{ID: "observe", Type: "network.check", TargetID: "node", Payload: json.RawMessage("{}")})
	if err == nil {
		t.Fatal("submission bypassed NATS")
	}
}
