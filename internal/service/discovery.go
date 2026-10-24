package service

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"

	"nixmox/internal/domain"
	"nixmox/internal/engine"
)

func (s *Service) DiscoveryRecords() []domain.Discovery {
	c := s.Cluster
	c.mu.Lock()
	defer c.mu.Unlock()
	id := c.keys.Identity.NodeID
	records := []domain.Discovery{{ID: "discovery_" + id, Kind: "node", NodeID: &id, Name: c.host.Name, Endpoints: c.host.Endpoints, KeyFingerprint: c.keys.DaemonKey()}}
	if c.creator() {
		cluster := c.keys.Trust.Cluster
		records = append(records, domain.Discovery{ID: "discovery_" + cluster.ID, Kind: "cluster", ClusterID: &cluster.ID, Name: cluster.Name, Endpoints: c.host.Endpoints, KeyFingerprint: c.keys.Trust.Issuance.ClusterFingerprint})
	}
	return records
}
func (s *Service) discoveryDefinition() engine.TaskDefinition {
	run := func(ctx context.Context, e engine.Execution) error {
		if s.Discovery == nil {
			return domain.Unavailable("discovery")
		}
		records, err := s.Discovery.Search(ctx)
		if err != nil {
			return err
		}
		return e.Result(ctx, records)
	}
	return engine.TaskDefinition{SafeRetry: true, Phase: "search", Interruptible: true, Run: run}
}

// Autojoin is a one-shot action on startup or a discovery event.
func (s *Service) Autojoin(ctx context.Context) error {
	c := s.Cluster
	c.mu.Lock()
	config, id, suppressed := c.config, c.keys.Identity, c.keys.AutojoinSuppressed()
	c.mu.Unlock()
	if config.Cluster.Name == nil || id.ClusterID != nil || suppressed {
		return nil
	}
	commandID := domain.ID("cmd_autojoin_" + uuid.NewSHA1(uuid.NameSpaceOID, []byte(id.NodeID)).String())
	if _, err := s.Receipt(ctx, commandID); err == nil {
		return nil
	}
	address := ""
	if config.Cluster.JoinAddress != nil {
		address = *config.Cluster.JoinAddress
	}
	if address == "" && s.Discovery != nil {
		matches := map[domain.ID]string{}
		for _, record := range s.Discovery.Snapshot() {
			if record.Kind == "cluster" && record.Name == *config.Cluster.Name && record.ClusterID != nil && len(record.Endpoints) > 0 {
				matches[*record.ClusterID] = record.Endpoints[len(record.Endpoints)-1].Address
			}
		}
		if len(matches) == 1 {
			for _, candidate := range matches {
				address = candidate
			}
		}
	}
	if address == "" {
		return nil
	}
	payload, _ := json.Marshal(domain.ClusterJoin{Address: address, ExpectedClusterName: *config.Cluster.Name})
	_, err := s.Submit(ctx, domain.Command{ID: commandID, Type: "cluster.join", TargetID: id.NodeID, Payload: payload})
	return err
}
