package service

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"nixmox/internal/domain"
	"nixmox/internal/engine"
	"nixmox/internal/engine/task"
)

// StartNode wires only implemented node-local handlers. It does not fabricate
// membership, cluster quorum or admission for unfinished operations.
func (s *Service) StartNode(ctx context.Context) error {
	check := func(ctx context.Context, execution engine.Execution) error {
		status, err := s.Network(ctx)
		if err != nil {
			return err
		}
		evidence, err := json.Marshal(status)
		if err != nil {
			return err
		}
		if _, err = fmt.Fprintf(execution.Log(), "Management network: %s\n", status.Network.Status); err != nil {
			return err
		}
		return execution.Observed(ctx, domain.Checkpoint{Phase: "observe", Evidence: evidence})
	}
	definitions := []engine.TaskDefinition{}
	for _, kind := range []string{"network.check", "host.refresh"} {
		definitions = append(definitions, engine.TaskDefinition{Kind: kind, SafeRetry: true, Phases: []engine.Phase{{Name: "observe", Interruptible: true, Run: check, Reconcile: check}}})
	}
	if s.Discovery != nil {
		definitions = append(definitions, s.discoveryDefinition())
	}
	if s.Cluster != nil {
		s.Cache = s.Cluster.cache
		definitions = append(definitions, s.powerDefinitions()...)
		definitions = append(definitions, s.Cluster.Definitions(s.Journal)...)
	}
	scheduler, err := task.NewScheduler(s.Journal, s.Identity.NodeID, definitions)
	if err != nil {
		return err
	}
	if s.Cluster != nil {
		scheduler.MembershipContext = s.Cluster.taskContext
	}
	s.Scheduler = scheduler
	return scheduler.Recover(ctx)
}

func (s *Service) Logs(ctx context.Context, id domain.ID, offset int64, limit int) (domain.LogChunk, error) {
	return s.taskLogs(ctx, id, offset, limit)
}

func (s *Service) Telemetry(ctx context.Context, host domain.ID) (engine.Subscription[domain.HostTelemetry], error) {
	if host != s.Identity.NodeID {
		if s.Cluster != nil {
			return s.Cluster.Telemetry(ctx, host)
		}
		return nil, &domain.Error{Code: "NOT_FOUND", Message: "Host not found"}
	}
	sub, err := s.Sampler.Subscribe(ctx)
	if err != nil || s.Cluster == nil {
		return sub, err
	}
	return &membershipTelemetry{Subscription: sub, cluster: s.Cluster, host: host}, nil
}

func (s *Service) Read(ctx context.Context, request ReadRequest) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path := request.Collection
	if request.ID != nil || request.Subresource != "" {
		path = ""
	}
	page, err := validatePage(path, request.Page)
	if err != nil {
		return nil, err
	}
	request.Page = page
	// Validate host predicates before forwarding, even if the authority is offline.
	if request.Collection == "hosts" && request.ID == nil {
		if _, err := parseHostFilter(page.Filters); err != nil {
			return nil, err
		}
	}
	if s.Cluster != nil && s.Cluster.leaf() && (request.Collection == "hosts" || request.Collection == "cluster/status") {
		localHost := request.Collection == "hosts" && request.ID != nil && *request.ID == s.Identity.NodeID && request.Subresource == "telemetry"
		if !localHost {
			return s.Cluster.call(ctx, "", "read", request, true)
		}
	}
	switch request.Collection {
	case "cache/status":
		if s.Cache == nil {
			return nil, cacheUnavailable()
		}
		return json.Marshal(s.Cache.Status(ctx))
	case "cache/locations":
		if s.Cache == nil {
			return nil, cacheUnavailable()
		}
		locations, err := s.Cache.Locate(ctx, request.Page.Filters["path"])
		if err != nil {
			return nil, err
		}
		return json.Marshal(locations)
	case "credentials":
		if s.Cluster == nil {
			return nil, credentialMissing()
		}
		if s.Cluster.leaf() {
			return s.Cluster.call(ctx, "", "read", request, true)
		}
		return s.Cluster.readCredentials(ctx, request)
	case "repositories", "configurations":
		if s.Cluster == nil {
			return nil, domain.Unavailable("repository metadata")
		}
		if s.Cluster.leaf() {
			return s.Cluster.call(ctx, "", "read", request, true)
		}
		return s.Cluster.readSources(ctx, request)
	case "discoveries":
		items := []domain.Discovery{}
		if s.Discovery != nil {
			items = s.Discovery.Snapshot()
		}
		kind, err := enumFilter(request.Page.Filters["kind"], "node", "cluster")
		if err != nil {
			return nil, err
		}
		items = slices.DeleteFunc(slices.Clone(items), func(d domain.Discovery) bool { return kind != "" && string(d.Kind) != kind })
		slices.SortFunc(items, func(a, b domain.Discovery) int { return cmp.Compare(a.ID, b.ID) })
		page, err := pageItems(items, request.Page, "discoveries", func(d domain.Discovery) []string { return []string{string(d.ID)} })
		if err != nil {
			return nil, err
		}
		return json.Marshal(page)
	case "identity":
		if s.Cluster != nil {
			return json.Marshal(s.Cluster.Identity())
		}
		return json.Marshal(s.Identity)
	case "cluster/requests":
		if s.Cluster == nil {
			return nil, domain.Unavailable("cluster requests")
		}
		page, err := s.Cluster.Requests(ctx, request.Page)
		if err != nil {
			return nil, err
		}
		return json.Marshal(page)
	case "cluster/status":
		if s.Cluster != nil {
			return json.Marshal(s.Cluster.Status(ctx))
		}
		return json.Marshal(domain.ClusterStatus{Membership: domain.Unjoined,
			Observed: domain.Observed{Freshness: domain.Unknown},
			Health:   domain.Health{Status: "unknown", Reasons: []domain.HealthReason{}}})
	case "network/status":
		status, err := s.Network(ctx)
		if err != nil {
			return nil, err
		}
		return json.Marshal(status)
	case "hosts":
		if s.Cluster != nil {
			return s.readHosts(ctx, request)
		}
		host := s.Host
		now := time.Now().UTC()
		host.LastSeen = &now
		if request.ID == nil {
			page, err := hostPage([]domain.Host{host}, request.Page)
			if err != nil {
				return nil, err
			}
			return json.Marshal(page)
		}
		if *request.ID != s.Identity.NodeID {
			return nil, &domain.Error{Code: "NOT_FOUND", Message: "Host not found"}
		}
		if request.Subresource == "telemetry" {
			return json.Marshal(s.Sampler.Snapshot(ctx))
		}
		return json.Marshal(host)
	case "tasks":
		if request.ID == nil {
			tasks, err := s.taskList(ctx, request.Page)
			if err != nil {
				return nil, err
			}
			return json.Marshal(tasks)
		}
		current, err := s.taskState(ctx, *request.ID)
		if err != nil {
			return nil, err
		}
		return json.Marshal(current)
	}
	return nil, &domain.Error{Code: "NOT_FOUND", Message: "Resource not found"}
}
