package service

import (
	"context"
	"encoding/json"
	"time"

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
		data, err := json.Marshal(records)
		if err != nil {
			return err
		}
		return e.Observed(ctx, domain.Checkpoint{Phase: "search", Evidence: data})
	}
	return engine.TaskDefinition{Kind: "discovery.search", Phases: []engine.Phase{{Name: "search", Interruptible: true, Run: run, Reconcile: run}}}
}

// One automatic intent per identity. Rejection/expiry never generates a new
// request on a timer; explicit Join is the administrator's next-attempt action.
func (s *Service) Autojoin(ctx context.Context) error {
	c := s.Cluster
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
		c.mu.Lock()
		config, id := c.config, c.keys.Identity
		suppressed := c.keys.AutojoinSuppressed()
		c.mu.Unlock()
		if config.Cluster.Name == nil || id.ClusterID != nil || suppressed {
			continue
		}
		commandID := domain.ID("cmd_autojoin_" + uuid.NewSHA1(uuid.NameSpaceOID, []byte(id.NodeID)).String())
		if _, err := s.Journal.Receipt(ctx, commandID); err == nil {
			continue
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
				for _, value := range matches {
					address = value
				}
			}
		}
		if address == "" {
			continue
		}
		data, err := json.Marshal(domain.ClusterJoin{Address: address, ExpectedClusterName: *config.Cluster.Name})
		if err != nil {
			return err
		}
		_, _ = s.Submit(ctx, domain.Command{ID: commandID, Type: "cluster.join", TargetID: id.NodeID, Payload: data})
	}
}
