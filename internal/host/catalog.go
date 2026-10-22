package host

import (
	"context"
	"encoding/json"
	"errors"
	"nixmox/internal/domain"
	"nixmox/internal/service"
	"nixmox/internal/state"
	"sort"
	"time"
)

type Catalog struct {
	State          state.Store
	Identity       func() domain.Identity
	Members        func(context.Context) ([]domain.HostMembership, error)
	Sample         func() domain.HostSample
	Configurations service.ConfigurationReader
}
type bindingRecord struct {
	Version  int64              `json:"version"`
	Epoch    int                `json:"membership_epoch"`
	Binding  domain.HostBinding `json:"binding"`
	Commands map[string]int64   `json:"commands"`
}

func missing(err error) bool { var e *domain.Error; return errors.As(err, &e) && e.Code == "NOT_FOUND" }
func (c *Catalog) membership(ctx context.Context, id string) (domain.HostMembership, error) {
	members, err := c.Members(ctx)
	if err != nil {
		return domain.HostMembership{}, err
	}
	for _, m := range members {
		if m.NodeID == id {
			return m, nil
		}
	}
	return domain.HostMembership{}, domain.Err("NOT_FOUND", "Host is not in this cluster.", 404)
}
func (c *Catalog) Host(ctx context.Context, id string) (domain.Host, error) {
	if !domain.SafeID.MatchString(id) {
		return domain.Host{}, domain.Err("INVALID_ID", "Invalid host ID.", 422)
	}
	identity := c.Identity()
	var member domain.HostMembership
	var err error
	if identity.ClusterID == nil && id == identity.NodeID {
		member = domain.HostMembership{NodeID: id, Version: 1, State: "unjoined", BrokerRole: "standalone"}
	} else {
		member, err = c.membership(ctx, id)
		if err != nil {
			return domain.Host{}, err
		}
	}
	h := domain.Host{SchemaVersion: 1, ID: id, Version: int64(member.Epoch)*1000000000 + member.Version, Name: id, Membership: member.State, MembershipEpoch: member.Epoch, Environment: "unknown", Reachability: "offline", Health: domain.Health{Status: "unknown", Reasons: []domain.HealthReason{}}, PendingReasons: []string{"configuration_not_deployed"}, Endpoints: []domain.Endpoint{}, ActiveTaskIDs: []string{}, Connectivity: []domain.Connectivity{}, BrokerRole: member.BrokerRole}
	if member.ClusterID != "" {
		h.ClusterID = &member.ClusterID
	}
	if member.State != "unjoined" {
		var binding bindingRecord
		_, err = c.State.Get(ctx, "host_binding."+id, &binding)
		if err != nil && !missing(err) {
			return h, err
		}
		if err == nil && binding.Epoch == member.Epoch {
			h.Configuration = &binding.Binding
			h.Version += binding.Version
		}
	}
	var sample domain.HostSample
	if id == identity.NodeID && c.Sample != nil {
		sample = c.Sample()
	} else {
		var observation domain.HostObservation
		_, err = c.State.Get(ctx, "observation."+id, &observation)
		if err != nil && !missing(err) {
			return h, err
		}
		if observation.Epoch == member.Epoch {
			sample = observation.Sample
		}
	}
	if sample.SchemaVersion == 1 && sample.HostID == id {
		h.Name = sample.Name
		h.System = sample.System
		h.Environment = sample.Environment
		h.RunningOutput = sample.RunningOutput
		h.LastSeen = &sample.ObservedAt
		h.Health = sample.Health
		h.Endpoints = sample.Endpoints
		h.Capabilities = sample.Capabilities
		age := time.Since(sample.ObservedAt)
		if age <= 45*time.Second {
			h.Reachability = "online"
		} else {
			h.Health.Status = "unknown"
			if age <= 2*time.Minute {
				h.Reachability = "stale"
			}
		}
	}
	if member.State == "removed" {
		h.Reachability = "offline"
		h.Capabilities = domain.HostCapabilities{}
	}
	return h, nil
}
func (c *Catalog) Hosts(ctx context.Context, limit, cursor string) (domain.Page[domain.Host], error) {
	identity := c.Identity()
	members := []domain.HostMembership{{NodeID: identity.NodeID}}
	if identity.ClusterID != nil {
		var err error
		members, err = c.Members(ctx)
		if err != nil {
			return domain.Page[domain.Host]{}, err
		}
	}
	rows := []domain.Host{}
	for _, m := range members {
		h, err := c.Host(ctx, m.NodeID)
		if err != nil {
			return domain.Page[domain.Host]{}, err
		}
		rows = append(rows, h)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	return domain.Paginate(rows, "hosts", limit, cursor, func(h domain.Host) string { return h.ID })
}
func (c *Catalog) Operations() []service.Operation {
	type payload struct {
		HostID        string `json:"host_id"`
		RepositoryID  string `json:"repository_id"`
		Configuration string `json:"configuration"`
		RefID         string `json:"ref_id,omitempty"`
	}
	type resolution struct {
		HostID        string               `json:"host_id"`
		Epoch         int                  `json:"membership_epoch"`
		HostVersion   int64                `json:"host_version"`
		Configuration domain.Configuration `json:"configuration"`
	}
	op := service.Operation{Kind: "host.assign", Scope: "cluster", RunOnCoordinator: true, Timeout: 30 * time.Second, SafeRetry: true}
	op.Validate = func(cmd domain.Command) error {
		var p payload
		if domain.StrictJSON(cmd.Payload, &p) != nil || p.HostID != cmd.TargetID || !domain.SafeID.MatchString(p.RepositoryID) || p.Configuration == "" || len(cmd.ExpectedVersions) != 2 || cmd.ExpectedVersions["host:"+p.HostID] < 1 {
			return domain.Err("INVALID_PAYLOAD", "Assignment requires host, repository, configuration and reviewed host/configuration versions.", 422)
		}
		return nil
	}
	op.Resolve = func(ctx context.Context, cmd domain.Command) (json.RawMessage, error) {
		var p payload
		_ = json.Unmarshal(cmd.Payload, &p)
		h, err := c.Host(ctx, p.HostID)
		if err != nil {
			return nil, err
		}
		if h.Version != cmd.ExpectedVersions["host:"+h.ID] {
			return nil, domain.Err("VERSION_CONFLICT", "Host binding changed after review.", 409)
		}
		page, err := c.Configurations.Configurations(ctx, p.RepositoryID, p.RefID, "", "500", "")
		if err != nil {
			return nil, err
		}
		for _, cfg := range page.Items {
			if cfg.ID == p.Configuration || cfg.Name == p.Configuration {
				if cmd.ExpectedVersions["configuration:"+cfg.ID] != cfg.Version {
					return nil, domain.Err("VERSION_CONFLICT", "Configuration changed after review.", 409)
				}
				if err = c.Configurations.CheckExport(ctx, cfg); err != nil {
					return nil, err
				}
				b, _ := json.Marshal(resolution{h.ID, h.MembershipEpoch, h.Version, cfg})
				return b, nil
			}
		}
		return nil, domain.Err("NOT_FOUND", "Selected ref does not export the configuration.", 404)
	}
	run := func(ctx context.Context, e *service.Execution) (any, error) {
		var v resolution
		if domain.StrictJSON(e.Resolved, &v) != nil {
			return nil, domain.Err("INVALID_STATE", "Invalid assignment resolution.", 409)
		}
		var binding bindingRecord
		revision, err := c.State.Get(ctx, "host_binding."+v.HostID, &binding)
		if err != nil && !missing(err) {
			return nil, err
		}
		if err != nil {
			revision = 0
		}
		if binding.Epoch == v.Epoch {
			if _, ok := binding.Commands[e.Command.ID]; ok {
				return binding.Binding, nil
			}
		}
		h, err := c.Host(ctx, v.HostID)
		if err != nil {
			return nil, err
		}
		if h.Version != v.HostVersion || h.MembershipEpoch != v.Epoch {
			return nil, domain.Err("VERSION_CONFLICT", "Host changed after assignment admission.", 409)
		}
		if binding.Epoch != v.Epoch {
			binding = bindingRecord{Epoch: v.Epoch, Commands: map[string]int64{}}
		}
		if binding.Commands == nil {
			binding.Commands = map[string]int64{}
		}
		binding.Version++
		binding.Binding = domain.HostBinding{RepositoryID: v.Configuration.RepositoryID, Name: v.Configuration.Name, RefID: v.Configuration.RefID}
		binding.Commands[e.Command.ID] = binding.Version
		if _, err = c.State.CAS(ctx, "host_binding."+v.HostID, binding, revision); err != nil {
			return nil, err
		}
		return binding.Binding, nil
	}
	op.Run = run
	op.Reconcile = run
	op.Exclusions = func(cmd domain.Command, _ json.RawMessage) ([]string, error) {
		return []string{"host_binding:" + cmd.TargetID}, nil
	}
	return []service.Operation{op}
}
