package comms

import (
	"context"
	"errors"

	js "github.com/nats-io/nats.go/jetstream"
	"nixmox/internal/domain"
	"nixmox/internal/engine/internal/shared"
)

func durableStreams() []string {
	return []string{"KV_" + domain.StateBucket, commandStream, resultStream, "OBJ_" + domain.GitBucket}
}

// Expansion is explicit and idempotent; a partial update is completed on retry.
// Never reduce replicas on startup, missing peers, or loss of quorum.
func (j *JetStream) ExpandReplicas(ctx context.Context) error {
	for _, name := range durableStreams() {
		stream, err := j.API.Stream(ctx, name)
		if err != nil {
			return err
		}
		info, err := stream.Info(ctx)
		if err != nil {
			return err
		}
		if info.Config.Replicas == 3 {
			continue
		}
		if info.Config.Replicas != 1 {
			return errors.New("unexpected replica configuration")
		}
		config := info.Config
		config.Replicas = 3
		if _, err = j.API.UpdateStream(ctx, config); err != nil {
			return err
		}
	}
	return nil
}
func liveReplicas(info *js.StreamInfo, except string) int {
	if info.Cluster == nil {
		if except == "" {
			return 1
		}
		return 0
	}
	count := 0
	if info.Cluster.Leader != "" && info.Cluster.Leader != except {
		count++
	}
	for _, peer := range info.Cluster.Replicas {
		if peer.Name != except && peer.Current && !peer.Offline {
			count++
		}
	}
	return count
}
func (j *JetStream) ReplicasReady(ctx context.Context, count int) error {
	for _, name := range durableStreams() {
		stream, err := j.API.Stream(ctx, name)
		if err != nil {
			return err
		}
		info, err := stream.Info(ctx)
		if err != nil {
			return err
		}
		if info.Config.Replicas != count || liveReplicas(info, "") != count {
			return errors.New("durable replicas are not all current")
		}
	}
	return nil
}
func (j *JetStream) CheckOutage(ctx context.Context, node domain.ID) error {
	for _, name := range durableStreams() {
		stream, err := j.API.Stream(ctx, name)
		if err != nil {
			return err
		}
		info, err := stream.Info(ctx)
		if err != nil {
			return err
		}
		if liveReplicas(info, shared.Token(node)) < info.Config.Replicas/2+1 {
			return &domain.Error{Code: "QUORUM_UNAVAILABLE", Message: "Power operation would lose durable quorum"}
		}
	}
	return nil
}
