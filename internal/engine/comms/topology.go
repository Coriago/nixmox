package comms

import (
	"context"
	"errors"
	"fmt"

	js "github.com/nats-io/nats.go/jetstream"
	"nixmox/internal/domain"
	"nixmox/internal/engine/internal/shared"
)

func durableStreams() []string {
	return []string{"KV_" + domain.StateBucket, commandStream, resultStream, "OBJ_" + domain.ObjectBucket}
}

// Only an explicit membership operation may change replication. Configuration
// and observed peers are separate: unavailable replicas never imply a downscale.
func (j *JetStream) setReplicas(ctx context.Context, replicas int) error {
	if replicas != 1 && replicas != 3 {
		return errors.New("only one or three replicas are supported")
	}
	for _, name := range durableStreams() {
		stream, err := j.API.Stream(ctx, name)
		if err != nil {
			return err
		}
		info, err := stream.Info(ctx)
		if err != nil {
			return err
		}
		if info.Config.Replicas == replicas {
			continue
		}
		if info.Config.Replicas != 1 && info.Config.Replicas != 3 {
			return errors.New("unexpected replica configuration")
		}
		config := info.Config
		config.Replicas = replicas
		if _, err = j.API.UpdateStream(ctx, config); err != nil {
			return err
		}
	}
	return nil
}
func (j *JetStream) ExpandReplicas(ctx context.Context) error { return j.setReplicas(ctx, 3) }

func currentReplicas(cluster *js.ClusterInfo, except string) int {
	if cluster == nil {
		if except == "" {
			return 1
		}
		return 0
	}
	count := 0
	if cluster.Leader != "" && cluster.Leader != except {
		count++
	}
	for _, peer := range cluster.Replicas {
		if peer.Name != except && peer.Current && !peer.Offline {
			count++
		}
	}
	return count
}
func liveReplicas(info *js.StreamInfo, except string) int {
	return currentReplicas(info.Cluster, except)
}
func replicasCurrent(cluster *js.ClusterInfo, count int) bool {
	if cluster == nil {
		return count == 1
	}
	return cluster.Desired == nil && len(cluster.Replicas)+1 == count && currentReplicas(cluster, "") == count
}
func containsPeer(cluster *js.ClusterInfo, name string) bool {
	if cluster == nil {
		return false
	}
	if cluster.Leader == name {
		return true
	}
	for _, peer := range cluster.Replicas {
		if peer.Name == name {
			return true
		}
	}
	return false
}
func (j *JetStream) ReplicasReady(ctx context.Context, count int) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if count != 1 && count != 3 {
		return errors.New("invalid replica count")
	}
	for _, name := range durableStreams() {
		stream, err := j.API.Stream(ctx, name)
		if err != nil {
			return err
		}
		info, err := stream.Info(ctx)
		if err != nil {
			return err
		}
		if info.Config.Replicas != count || !replicasCurrent(info.Cluster, count) {
			return fmt.Errorf("%s: %w", name, errNotReady)
		}
		consumers := stream.ListConsumers(ctx)
		for consumer := range consumers.Info() {
			expected := consumer.Config.Replicas
			if expected == 0 {
				expected = count
			}
			if !replicasCurrent(consumer.Cluster, expected) {
				return fmt.Errorf("consumer %s: %w", consumer.Name, errNotReady)
			}
		}
		if err = consumers.Err(); err != nil {
			return err
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

// PrepareDeparture is an explicit, serialized planned-removal operation, not a
// liveness callback. Keep all peers connected while this completes. R1 assets
// must be evacuated before metadata removal; shrinking alone can leave the only
// copy on the departing server. NATS performs the data/consumer migration.
func (r *Runtime) PrepareDeparture(ctx context.Context, target domain.ID, remaining int) error {
	if target == "" || remaining < 1 {
		return errors.New("removal requires a target and at least one survivor")
	}
	if r.Store == nil {
		return errors.New("JetStream storage is not attached")
	}
	count := 3
	if remaining < 3 {
		count = 1
	}
	if err := r.Store.setReplicas(ctx, count); err != nil {
		return err
	}
	if err := await(ctx, func() error { return r.Store.ReplicasReady(ctx, count) }); err != nil {
		return err
	}
	_, manager, close, err := r.Broker.admin(ctx, r.options.NodeID, r.clientTLS, false)
	if err != nil {
		return err
	}
	defer close()
	name := shared.Token(target)
	for _, streamName := range durableStreams() {
		err = await(ctx, func() error {
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			stream, err := r.Store.API.Stream(ctx, streamName)
			if err != nil {
				return err
			}
			info, err := stream.Info(ctx)
			if err != nil {
				return err
			}
			if containsPeer(info.Cluster, name) {
				managed, err := manager.LoadStream(streamName)
				if err != nil {
					return err
				}
				if err = managed.EvacuatePeer(name); err != nil {
					return err
				}
				return errNotReady
			}
			if !replicasCurrent(info.Cluster, count) {
				return errNotReady
			}
			consumers := stream.ListConsumers(ctx)
			for consumer := range consumers.Info() {
				if containsPeer(consumer.Cluster, name) {
					return errNotReady
				}
			}
			return consumers.Err()
		})
		if err != nil {
			return err
		}
	}
	return nil
}
