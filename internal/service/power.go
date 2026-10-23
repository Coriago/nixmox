package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	js "github.com/nats-io/nats.go/jetstream"

	"nixmox/internal/domain"
	"nixmox/internal/engine"
	"nixmox/internal/engine/platform"
	"nixmox/internal/engine/task"
)

func isPower(kind string) bool { return kind == "host.shutdown" || kind == "host.reboot" }
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

// reconcilePower consumes durable executor evidence, not daemon startup itself.
// Run after task projection so a failed membership CAS is retried even when the
// task is already terminal. Only the authority can release the reservation.
func (c *ClusterNode) reconcilePower(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.creator() || c.state == nil || c.state.Power == nil {
		return nil
	}
	r := *c.state.Power
	if err := c.quorum(ctx); err != nil {
		return err
	}
	current, _, err := readRecord[domain.Task](ctx, c.transport.Store, recordKey("task", task.TaskID(r.CommandID)))
	if errors.Is(err, js.ErrKeyNotFound) {
		return nil
	} // Admission/projection may still be pending.
	if err != nil {
		return err
	}
	if current.ID != task.TaskID(r.CommandID) || current.CommandID != r.CommandID || current.OwnerNodeID != r.HostID || current.TargetID != r.HostID || current.MembershipEpoch != r.Epoch || !isPower(current.Kind) || current.Scope != domain.ClusterScope || current.ClusterID == nil || *current.ClusterID != c.state.Cluster.ID {
		return nil
	}
	for _, cp := range current.Checkpoints {
		if cp.State != "observed" {
			continue
		}
		var evidence struct {
			PreviousBootID string `json:"previous_boot_id"`
			BootID         string `json:"boot_id"`
			Returned       bool   `json:"returned_after_request"`
			Scheduled      *bool  `json:"scheduled"`
		}
		if json.Unmarshal(cp.Evidence, &evidence) != nil {
			continue
		}
		rebooted := current.State == domain.Succeeded && cp.Phase == "boot" && evidence.Returned && r.BootID != "" && evidence.PreviousBootID == r.BootID && evidence.BootID != "" && evidence.BootID != r.BootID
		neverScheduled := current.State == domain.Failed && cp.Phase == "schedule" && evidence.Scheduled != nil && !*evidence.Scheduled
		if rebooted || neverScheduled {
			state := *c.state
			state.Power = nil
			return c.saveState(ctx, state)
		}
	}
	return nil
}

func (s *Service) powerDefinitions() []engine.TaskDefinition {
	definitions := []engine.TaskDefinition{}
	for _, kind := range []string{"host.shutdown", "host.reboot"} {
		schedule := func(ctx context.Context, e engine.Execution) error {
			fail := func(err error) error {
				data, _ := json.Marshal(map[string]any{"scheduled": false, "reason": err.Error()})
				return errors.Join(err, e.Observed(ctx, domain.Checkpoint{Phase: "schedule", Evidence: data}))
			}
			info, err := s.Cluster.powerInfo()
			if err != nil {
				return fail(err)
			}
			if !info.Enabled {
				return fail(errors.New("executor power capability is disabled"))
			}
			if _, err = platform.PowerPlan(kind); err != nil {
				return fail(err)
			}
			var reservation domain.PowerReservation
			if s.Cluster.leaf() {
				var data json.RawMessage
				data, err = s.Cluster.call(ctx, "", "power.authorize", e.Task().CommandID, true)
				if err == nil {
					err = json.Unmarshal(data, &reservation)
				}
			} else {
				reservation, err = s.Cluster.authorizePower(ctx, s.Identity.NodeID, e.Task().CommandID)
			}
			if err != nil {
				return err
			} // No effect on uncertainty; retry authorization.
			if reservation.BootID != info.BootID || reservation.Epoch != info.Epoch {
				return fail(errors.New("OS boot or membership changed before power scheduling"))
			}
			evidence, _ := json.Marshal(reservation)
			if err = e.Intent(ctx, domain.Checkpoint{Phase: "schedule", Evidence: evidence}); err != nil {
				return err
			}
			if err = platform.SchedulePower(ctx, kind, e, task.ProcessRunner{}); err != nil {
				return err
			}
			fmt.Fprintln(e.Log(), "Power transition scheduled in one minute; completion requires a different OS boot ID")
			return e.Observed(ctx, domain.Checkpoint{Phase: "schedule", Evidence: evidence})
		}
		inspect := func(ctx context.Context, e engine.Execution) error {
			var reservation domain.PowerReservation
			for _, cp := range e.Task().Checkpoints {
				if cp.Phase == "schedule" {
					if err := json.Unmarshal(cp.Evidence, &reservation); err != nil {
						return err
					}
				}
			}
			boot, err := platform.BootID()
			if err != nil {
				return err
			}
			if reservation.BootID == "" || boot == reservation.BootID {
				return errors.New("power transition not observed; do not repeat shutdown scheduling")
			}
			data, _ := json.Marshal(map[string]any{"previous_boot_id": reservation.BootID, "boot_id": boot, "returned_after_request": true})
			return e.Observed(ctx, domain.Checkpoint{Phase: e.Task().Phase, Evidence: data})
		}
		reconcile := func(ctx context.Context, e engine.Execution) error {
			for _, cp := range e.Task().Checkpoints {
				if cp.Phase == "schedule" && len(cp.Evidence) > 0 {
					// An intent with boot evidence might have invoked shutdown. Never replay it.
					boot, err := platform.BootID()
					if err != nil {
						return err
					}
					var r domain.PowerReservation
					if err = json.Unmarshal(cp.Evidence, &r); err != nil {
						return err
					}
					if r.BootID == boot {
						return errors.New("power scheduling outcome remains uncertain")
					}
					return e.Observed(ctx, domain.Checkpoint{Phase: "schedule", Evidence: cp.Evidence})
				}
			}
			return schedule(ctx, e)
		}
		resources := []engine.Resource{{Key: "host:" + string(s.Identity.NodeID), Capacity: 1}}
		definitions = append(definitions, engine.TaskDefinition{Kind: kind, Phases: []engine.Phase{{Name: "schedule", Resources: resources, Run: schedule, Reconcile: reconcile}, {Name: "boot", Resources: resources, Run: inspect, Reconcile: inspect}}})
	}
	return definitions
}
