package cluster

import (
	"context"
	"log"
)

// ReplicaScaler promotes KV buckets from R=1 to R=3 once the cluster has
// enough JetStream peers. Never scales down (avoids flapping); never R=2.
type ReplicaScaler struct {
	Buckets   []string
	PeerCount func() int
	Scale     func(ctx context.Context, bucket string, replicas int) error
}

func (s ReplicaScaler) Check(ctx context.Context) {
	peers := s.PeerCount()
	if peers < 3 {
		return
	}
	for _, b := range s.Buckets {
		if err := s.Scale(ctx, b, 3); err != nil {
			log.Printf("replica scale %s: %v", b, err)
		}
	}
}
