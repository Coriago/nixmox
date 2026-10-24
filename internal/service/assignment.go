package service

import (
	"context"
	"encoding/json"
	"maps"

	"nixmox/internal/domain"
	"nixmox/internal/engine"
)

func (c *ClusterNode) validateAssignment(ctx context.Context, command domain.Command, p domain.HostAssign) error {
	if err := c.quorum(ctx); err != nil {
		return err
	}
	member, ok := c.state.Members[p.HostID]
	if !ok || member.Removed || p.HostID != command.TargetID {
		return invalid("Target must be an enrolled host matching host_id")
	}
	if p.RepositoryID == "" || p.Configuration == "" {
		return invalid("Repository and configuration are required")
	}
	if _, done := member.Assignments[command.ID]; done {
		return nil
	}
	state, _, err := c.repositoryLocked(ctx, p.RepositoryID)
	if err != nil {
		return err
	}
	configID := sourceID("cfg", string(p.RepositoryID), p.Configuration)
	if len(command.ExpectedVersions) != 2 || command.ExpectedVersions["host:"+string(p.HostID)] != member.Version || command.ExpectedVersions["configuration:"+string(configID)] != state.Repository.Version {
		return &domain.Error{Code: "VERSION_CONFLICT", Message: "Review the current host and configuration versions"}
	}
	if c.state.Power != nil && c.state.Power.HostID == p.HostID {
		return invalid("Host has an unresolved power transition")
	}
	if c.config.Configuration != nil && p.HostID == c.host.ID && (c.config.Configuration.Repository != state.Repository.Name || c.config.Configuration.Name != p.Configuration) {
		return &domain.Error{Code: "CONFIGURATION_CONFLICT", Message: "Host configuration is declared by runtime settings"}
	}
	return nil
}

func (c *ClusterNode) assignmentDefinition() engine.TaskDefinition {
	return engine.TaskDefinition{Phase: "assign", Resources: []string{"membership", "repositories"}, Run: c.assignConfiguration}
}

func (c *ClusterNode) assignConfiguration(ctx context.Context, e engine.Execution) error {
	command := e.Envelope().Command
	var p domain.HostAssign
	if err := json.Unmarshal(command.Payload, &p); err != nil {
		return err
	}
	c.mu.Lock()
	err := c.quorum(ctx)
	if err == nil {
		err = c.loadMembership(ctx)
	}
	if err != nil {
		c.mu.Unlock()
		return err // The acceptance proof is not yet readable.
	}
	err = c.validateAssignment(ctx, command, p)
	version, done := c.state.Members[p.HostID].Assignments[command.ID]
	c.mu.Unlock()
	if err == nil && !done {
		var binding domain.Binding
		binding, err = c.inspectAssignment(ctx, p)
		if err == nil {
			version, err = c.acceptAssignment(ctx, command, p, binding)
		}
	}
	if err != nil {
		return err
	}
	return e.Result(ctx, map[string]any{"host_id": p.HostID, "version": version})
}

// Inspect the catalog and its selected export in one pinned checkout.
func (c *ClusterNode) inspectAssignment(ctx context.Context, p domain.HostAssign) (domain.Binding, error) {
	binding := domain.Binding{RepositoryID: p.RepositoryID, Name: p.Configuration}
	state, err := c.repository(ctx, p.RepositoryID)
	if err != nil {
		return binding, err
	}
	var ref domain.ID
	if p.RefID != nil {
		ref = *p.RefID
	}
	source, err := configurationSource(state, ref, "")
	if err != nil {
		return binding, err
	}
	binding.RefID = *source.RefID
	source.Configuration = p.Configuration
	err = c.checkConfigurationSource(ctx, source)
	err = c.onSourceHolder(ctx, state.Repository, err, func(holder domain.ID) error {
		_, err := c.call(ctx, holder, "source.check", source, false)
		return err
	})
	return binding, err
}

func (c *ClusterNode) checkConfigurationSource(ctx context.Context, source domain.Source) error {
	return c.withSource(ctx, source, func(path string) error {
		descriptors, err := c.nix.Configurations(ctx, source, path)
		if err != nil {
			return &domain.Error{Code: "CATALOG_UNAVAILABLE", Message: "Pinned configuration catalog could not be inspected; no revision was substituted"}
		}
		for _, d := range descriptors {
			if d.Name == source.Configuration {
				source.FlakeDirectory, source.FlakeAttribute = d.FlakeDirectory, d.FlakeAttribute
				return c.nix.CheckConfiguration(ctx, source, path)
			}
		}
		return notFound()
	})
}

func (c *ClusterNode) acceptAssignment(ctx context.Context, command domain.Command, p domain.HostAssign, binding domain.Binding) (domain.Version, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Recheck mutable versions under the same mutex as the binding CAS.
	if err := c.validateAssignment(ctx, command, p); err != nil {
		return 0, err
	}
	member := c.state.Members[p.HostID]
	if version, done := member.Assignments[command.ID]; done {
		return version, nil
	}
	member.Configuration = &binding
	member.Version++
	member.Assignments = maps.Clone(member.Assignments)
	if member.Assignments == nil {
		member.Assignments = map[domain.ID]domain.Version{}
	}
	member.Assignments[command.ID] = member.Version
	next := *c.state
	next.Members = maps.Clone(next.Members)
	next.Members[p.HostID] = member
	return member.Version, c.saveState(ctx, next)
}
