package comms

import (
	"context"
	"nixmox/internal/domain"
	"time"
)

// Replication separates actual stream replicas from the metadata group size.
func (r *Runtime) Replication(ctx context.Context) domain.BrokerStatus {
	status := domain.BrokerStatus{Mode: string(r.StoreMode()), Streams: map[string]domain.StreamReplication{}}
	if r.Broker == nil {
		return status
	}
	if info, err := r.Broker.Diagnostics(); err == nil && info.Meta != nil {
		status.MetadataPeers = info.Meta.Size
		status.MetadataLeader = info.Meta.Leader
	}
	if r.Store == nil {
		return status
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	for _, name := range durableStreams() {
		stream, err := r.Store.API.Stream(ctx, name)
		if err != nil {
			continue
		}
		info, err := stream.Info(ctx)
		if err != nil {
			continue
		}
		status.Streams[name] = domain.StreamReplication{Replicas: info.Config.Replicas, Current: currentReplicas(info.Cluster, ""), Ready: replicasCurrent(info.Cluster, info.Config.Replicas)}
	}
	return status
}
