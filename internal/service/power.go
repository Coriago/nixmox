package service

import (
	"context"
	"encoding/json"
	"fmt"

	"nixmox/internal/domain"
	"nixmox/internal/engine"
	"nixmox/internal/engine/platform"
	"nixmox/internal/engine/task"
)

func (c *ClusterNode) powerInfo() (domain.PowerInfo, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	boot, err := platform.BootID()
	info := domain.PowerInfo{NodeID: c.keys.Identity.NodeID, Name: c.host.Name, Enabled: c.config.Capabilities.Power, BootID: boot}
	if c.keys.Identity.MembershipEpoch != nil {
		info.Epoch = *c.keys.Identity.MembershipEpoch
	}
	return info, err
}
func (c *ClusterNode) validateHost(command domain.Command, id domain.ID, epoch uint64, confirm, name string) error {
	member, ok := c.state.Members[id]
	if !ok || member.Removed || command.TargetID != id {
		return invalid("Target is not an active member")
	}
	if member.Epoch != epoch || len(command.ExpectedVersions) != 1 || command.ExpectedVersions["host:"+string(id)] != member.Version {
		return &domain.Error{Code: "VERSION_CONFLICT", Message: "Review the current host version and membership epoch"}
	}
	if confirm != string(id) && (name == "" || confirm != name) {
		return invalid("Confirm the exact host name or ID")
	}
	return nil
}
func (c *ClusterNode) validatePower(ctx context.Context, command domain.Command, p domain.HostPower) (domain.PowerInfo, error) {
	if err := c.quorum(ctx); err != nil {
		return domain.PowerInfo{}, err
	}
	name := c.inventory.name(p.HostID)
	if p.HostID == c.host.ID {
		name = c.host.Name
	}
	if err := c.validateHost(command, p.HostID, p.MembershipEpoch, p.ConfirmHost, name); err != nil {
		return domain.PowerInfo{}, err
	}
	if c.state.Power != nil && c.state.Power.CommandID != command.ID {
		return domain.PowerInfo{}, invalid("Another power transition is unresolved")
	}
	var info domain.PowerInfo
	var err error
	if p.HostID == c.host.ID {
		info = domain.PowerInfo{NodeID: c.host.ID, Name: c.host.Name, Enabled: c.config.Capabilities.Power, Epoch: p.MembershipEpoch}
		info.BootID, err = platform.BootID()
	} else {
		var data json.RawMessage
		data, err = fetchQuery(ctx, c.transport, c.keys.Identity, p.HostID, "power.inspect", []byte("{}"), false)
		if err == nil {
			err = json.Unmarshal(data, &info)
		}
	}
	if err != nil {
		return info, err
	}
	if info.NodeID != p.HostID || info.Epoch != p.MembershipEpoch {
		return info, invalid("Power observation does not match target membership")
	}
	if !info.Enabled {
		return info, &domain.Error{Code: "UNSUPPORTED_CAPABILITY", Message: "Power is disabled on the target executor"}
	}
	if err = c.safePowerOutage(ctx, p.HostID); err != nil {
		return info, err
	}
	return info, nil
}
func (c *ClusterNode) safePowerOutage(ctx context.Context, host domain.ID) error {
	if c.state.Topology == nil || len(c.state.Topology.Topology.Peers) < 3 {
		if host == c.state.Cluster.IssuerNodeID || c.state.Members[host].Full {
			return invalid("Power operation would interrupt the provisional control plane")
		}
		return nil
	}
	return c.transport.Store.CheckOutage(ctx, host)
}
func (c *ClusterNode) reservePower(ctx context.Context, command domain.Command) error {
	var p domain.HostPower
	if err := json.Unmarshal(command.Payload, &p); err != nil {
		return err
	}
	info, err := c.validatePower(ctx, command, p)
	if err != nil {
		return err
	}
	if c.state.Power != nil {
		return nil
	}
	state := *c.state
	state.Power = &domain.PowerReservation{CommandID: command.ID, HostID: p.HostID, Epoch: p.MembershipEpoch, BootID: info.BootID}
	return c.saveState(ctx, state)
}
func (c *ClusterNode) authorizePower(ctx context.Context, caller, id domain.ID) (domain.PowerReservation, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.quorum(ctx); err != nil {
		return domain.PowerReservation{}, err
	}
	r := c.state.Power
	if r == nil || r.CommandID != id || r.HostID != caller || c.state.Members[caller].Removed || c.state.Members[caller].Epoch != r.Epoch {
		return domain.PowerReservation{}, invalid("Power reservation no longer matches current membership")
	}
	if err := c.safePowerOutage(ctx, caller); err != nil {
		return *r, err
	}
	// A successful write, not a cached quorum observation, gates the effect.
	return *r, c.saveState(ctx, *c.state)
}

// Power tasks schedule once. Completion means the scheduling command returned,
// not that a reboot was observed. Membership keeps the outage reservation until
// a subsequent authenticated heartbeat reports a different OS boot.
func (s *Service) powerDefinition() engine.TaskDefinition {
	run := func(ctx context.Context, e engine.Execution) error {
		kind := e.Envelope().Command.Type
		info, err := s.Cluster.powerInfo()
		if err != nil {
			return err
		}
		if !info.Enabled {
			return domain.Unavailable("power")
		}
		if _, err := platform.PowerPlan(kind); err != nil {
			return err
		}
		var reservation domain.PowerReservation
		if s.Cluster.leaf() {
			err = s.Cluster.callResult(ctx, "", "power.authorize", e.Task().CommandID, true, &reservation)
		} else {
			reservation, err = s.Cluster.authorizePower(ctx, s.Identity.NodeID, e.Task().CommandID)
		}
		if err != nil {
			return err
		}
		if reservation.BootID != info.BootID || reservation.Epoch != info.Epoch {
			return invalid("Host changed before power scheduling")
		}
		if err := platform.SchedulePower(ctx, kind, e, task.ProcessRunner{}); err != nil {
			return err
		}
		fmt.Fprintln(e.Log(), "Power transition scheduled; this task will not be recovered or replayed after restart")
		return e.Result(ctx, map[string]any{"scheduled": true, "previous_boot_id": info.BootID})
	}
	return engine.TaskDefinition{Phase: "schedule", Resources: []string{"host:" + string(s.Identity.NodeID)}, Run: run}
}
