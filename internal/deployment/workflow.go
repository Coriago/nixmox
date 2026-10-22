package deployment

import (
	"context"
	"encoding/json"
	"errors"
	"nixmox/internal/domain"
	"nixmox/internal/gitstore"
	"nixmox/internal/service"
	"os"
	"path/filepath"
	"time"
)

type applyPayload struct {
	HostID       string    `json:"host_id,omitempty"`
	Source       *Selector `json:"source,omitempty"`
	CandidateID  string    `json:"candidate_id,omitempty"`
	BuilderID    string    `json:"builder_id,omitempty"`
	GenerationID string    `json:"generation_id,omitempty"`
	Mode         string    `json:"mode,omitempty"`
	AutoApprove  bool      `json:"auto_approve,omitempty"`
}
type applyPin struct {
	Build       *pinnedBuild      `json:"build"`
	CandidateID string            `json:"candidate_id"`
	Candidate   *domain.Candidate `json:"candidate,omitempty"`
	HostVersion int64             `json:"host_version"`
	HostEpoch   int               `json:"host_epoch"`
	Mode        string            `json:"mode"`
	AutoApprove bool              `json:"auto_approve"`
}
type workflowCheckpoint struct {
	Workflow         bool              `json:"workflow"`
	CandidateID      string            `json:"candidate_id"`
	DiffID           string            `json:"diff_id"`
	ProfileIntent    bool              `json:"profile_intent"`
	ActivationIntent bool              `json:"activation_intent"`
	Deployment       domain.Deployment `json:"deployment"`
}
type approvePayload struct {
	TaskID      string `json:"task_id"`
	DiffID      string `json:"diff_id"`
	DiffVersion int64  `json:"diff_version"`
}

func (s *Service) workflowOperations() []service.Operation {
	ops := []service.Operation{s.adoptOperation()}
	for _, kind := range []string{"deployment.apply", "deployment.rollback"} {
		ops = append(ops, service.Operation{Kind: kind, Scope: "cluster", RunOnCoordinator: true, Timeout: 2 * time.Hour, SafeRetry: false, Validate: func(c domain.Command) error {
			var p applyPayload
			if domain.StrictJSON(c.Payload, &p) != nil || p.HostID != "" && p.HostID != c.TargetID || len(c.ExpectedVersions) != 1 || c.ExpectedVersions["host:"+c.TargetID] < 1 {
				return domain.Err("INVALID_PAYLOAD", "Deployment requires the reviewed host version.", 422)
			}
			if p.Mode != "" && p.Mode != "switch" && p.Mode != "boot" {
				return domain.Err("INVALID_PAYLOAD", "Mode must be switch or boot.", 422)
			}
			if c.Type == "deployment.rollback" {
				if !domain.SafeID.MatchString(p.GenerationID) || p.CandidateID != "" || p.Source != nil || p.BuilderID != "" {
					return domain.Err("INVALID_PAYLOAD", "Rollback requires a retained generation.", 422)
				}
			} else if p.GenerationID != "" {
				return domain.Err("INVALID_PAYLOAD", "Apply does not accept generation_id.", 422)
			}
			if p.CandidateID != "" && (!domain.SafeID.MatchString(p.CandidateID) || p.Source != nil || p.BuilderID != "") {
				return domain.Err("INVALID_PAYLOAD", "Candidate cannot be combined with source or builder.", 422)
			}
			if p.Source != nil && p.Source.RefID != "" && p.Source.Commit != "" {
				return domain.Err("INVALID_SOURCE", "Ref and commit are mutually exclusive.", 422)
			}
			return nil
		}, Resolve: s.resolveApply, Run: s.runApply, Reconcile: s.runApply})
	}
	ops = append(ops, service.Operation{Kind: "deployment.diff", Scope: "cluster", RunOnCoordinator: true, Timeout: 2 * time.Minute, SafeRetry: true, Validate: func(c domain.Command) error {
		var p diffPayload
		if domain.StrictJSON(c.Payload, &p) != nil || p.HostID != c.TargetID || !domain.SafeID.MatchString(p.CandidateID) || p.BaselineID != "" && !domain.SafeID.MatchString(p.BaselineID) || len(c.ExpectedVersions) != 0 {
			return domain.Err("INVALID_PAYLOAD", "Diff requires host_id, candidate_id and optional baseline_generation_id.", 422)
		}
		return nil
	}, Run: s.runDiff, Reconcile: s.runDiff})
	ops = append(ops, service.Operation{Kind: "deployment.approve", Scope: "cluster", RunOnCoordinator: true, Timeout: 30 * time.Second, SafeRetry: true, Validate: func(c domain.Command) error {
		var p approvePayload
		if domain.StrictJSON(c.Payload, &p) != nil || !domain.SafeID.MatchString(p.TaskID) || !domain.SafeID.MatchString(p.DiffID) || p.DiffVersion < 1 || len(c.ExpectedVersions) != 0 {
			return domain.Err("INVALID_PAYLOAD", "Approval requires task_id, diff_id and diff_version.", 422)
		}
		return nil
	}, Run: s.runApprove, Reconcile: s.runApprove})
	return ops
}
func (s *Service) resolveApply(ctx context.Context, c domain.Command) (json.RawMessage, error) {
	var p applyPayload
	_ = json.Unmarshal(c.Payload, &p)
	if err := s.localTarget(c.TargetID); err != nil {
		return nil, err
	}
	h, err := s.Hosts.Host(ctx, c.TargetID)
	if err != nil {
		return nil, err
	}
	if h.Version != c.ExpectedVersions["host:"+h.ID] {
		return nil, domain.Err("VERSION_CONFLICT", "Host binding changed after review.", 409)
	}
	if !h.Capabilities.Apply || h.Environment == "installer" || h.Reachability != "online" {
		return nil, domain.Err("APPLY_UNAVAILABLE", "Apply requires a reachable installed NixOS activation environment.", 503)
	}
	if p.Mode == "" {
		p.Mode = "switch"
	}
	pin := applyPin{CandidateID: p.CandidateID, HostVersion: h.Version, HostEpoch: h.MembershipEpoch, Mode: p.Mode, AutoApprove: p.AutoApprove}
	if c.Type == "deployment.rollback" {
		g, lookupErr := s.lookupGeneration(ctx, h.ID, p.GenerationID)
		if lookupErr != nil {
			return nil, lookupErr
		}
		if g.HostID != h.ID {
			return nil, domain.Err("GENERATION_INVALID", "Generation belongs to another target.", 422)
		}
		info, err := s.Nix.Info(ctx, g.OutputPath)
		if err != nil {
			return nil, domain.Err("OUTPUT_UNAVAILABLE", "Retained rollback output is unavailable; rebuild it separately.", 503)
		}
		if info.NarHash != g.NarHash {
			return nil, domain.Err("OUTPUT_CHANGED", "Retained generation identity changed.", 409)
		}
		candidate := domain.Candidate{SchemaVersion: 1, ID: gitstore.StableID("cand_", c.ID), Version: 1, BuilderID: h.ID, System: h.System, State: "built", OutputPath: info.Path, NarHash: info.NarHash, CreatedAt: time.Now().UTC()}
		if g.Source != nil {
			candidate.Source = *g.Source
		}
		pin.Candidate = &candidate
		pin.CandidateID = candidate.ID
	} else if p.CandidateID != "" {
		candidate, err := s.Candidate(ctx, p.CandidateID)
		if err != nil {
			return nil, err
		}
		if candidate.State != "built" || candidate.System != h.System {
			return nil, domain.Err("PLATFORM_MISMATCH", "Candidate is not a built system for the target platform.", 422)
		}
		pin.Candidate = &candidate
	} else {
		buildCommand := c
		buildCommand.Type = "deployment.build"
		buildCommand.Payload, _ = json.Marshal(buildPayload{HostID: h.ID, Source: p.Source, BuilderID: p.BuilderID})
		resolved, err := s.resolveBuild(ctx, buildCommand)
		if err != nil {
			return nil, err
		}
		var b pinnedBuild
		if json.Unmarshal(resolved, &b) != nil {
			return nil, domain.Err("INVALID_STATE", "Invalid pinned build.", 409)
		}
		pin.Build = &b
		pin.CandidateID = gitstore.StableID("cand_", c.ID)
	}
	return json.Marshal(pin)
}
func (s *Service) runApprove(ctx context.Context, e *service.Execution) (any, error) {
	var p approvePayload
	_ = json.Unmarshal(e.Command.Payload, &p)
	d, err := s.readDiff(ctx, p.DiffID)
	if err != nil {
		return nil, err
	}
	if d.Diff.Version != p.DiffVersion {
		return nil, domain.Err("APPROVAL_STALE", "Comparison version changed.", 409)
	}
	if err = e.ApproveDeployment(p.TaskID, d.Diff); err != nil {
		return nil, err
	}
	return map[string]string{"task_id": p.TaskID, "diff_id": p.DiffID, "approval": "approved"}, nil
}
func (s *Service) Deployment(ctx context.Context, id string) (domain.Deployment, error) {
	var d domain.Deployment
	_, err := s.Sources.State.Get(ctx, "deployment."+id, &d)
	return d, err
}
func (s *Service) recordDeployment(ctx context.Context, d domain.Deployment) error {
	var old domain.Deployment
	rev, err := s.Sources.State.Get(ctx, "deployment."+d.ID, &old)
	if err != nil && !absent(err) {
		return err
	}
	_, err = s.Sources.State.CAS(ctx, "deployment."+d.ID, d, rev)
	return err
}
func (s *Service) runApply(ctx context.Context, e *service.Execution) (result any, failure error) {
	var pin applyPin
	if domain.StrictJSON(e.Resolved, &pin) != nil {
		return nil, domain.Err("INVALID_STATE", "Invalid deployment pin.", 409)
	}
	if err := s.localTarget(e.Command.TargetID); err != nil {
		return nil, err
	}
	task, err := e.Snapshot()
	if err != nil {
		return nil, err
	}
	var cp workflowCheckpoint
	defer func() {
		if failure == nil || !cp.ProfileIntent {
			return
		}
		var d *domain.Error
		if errors.As(failure, &d) && d.Code == "ACTIVATION_FAILED" {
			return
		}
		failure = service.ReconcileLater("OBSERVATION_UNKNOWN", "Target effects are uncertain; inspect the existing profile and activation receipt before continuing.")
	}()
	_ = json.Unmarshal(task.Checkpoint, &cp)
	if !cp.Workflow {
		var candidate domain.Candidate
		if pin.Candidate != nil {
			candidate = *pin.Candidate
			if err = s.immutable(ctx, "candidate."+candidate.ID, candidate); err != nil {
				return nil, err
			}
		} else {
			build := *e
			build.Resolved, _ = json.Marshal(pin.Build)
			value, err := s.runBuild(ctx, &build)
			if err != nil {
				return nil, err
			}
			candidate = value.(domain.Candidate)
		}
		if err = e.SetSource(candidate.Source, candidate.BuilderID); err != nil {
			return nil, err
		}
		diff, err := s.makeDiff(ctx, candidate, e.Command.TargetID, "")
		if err != nil {
			return nil, err
		}
		cp = workflowCheckpoint{Workflow: true, CandidateID: candidate.ID, DiffID: diff.ID, Deployment: domain.Deployment{ID: gitstore.StableID("dep_", e.Command.ID), TaskID: e.TaskID, TargetID: e.Command.TargetID, CandidateID: candidate.ID, DiffID: diff.ID, Mode: pin.Mode, Activation: "pending", Observation: "pending", OutputPath: candidate.OutputPath}}
		if err = e.Checkpoint("review", cp, true); err != nil {
			return nil, err
		}
	}
	candidate, err := s.Candidate(ctx, cp.CandidateID)
	if err != nil {
		return nil, err
	}
	if !cp.ProfileIntent {
		task, err = e.Snapshot()
		if err != nil {
			return nil, err
		}
		approval := task.Approval
		if approval == nil || approval.DiffID != cp.DiffID {
			a := domain.DeploymentApproval{AutoApprove: pin.AutoApprove, State: "pending", DiffID: cp.DiffID, DiffVersion: 1, CandidateID: cp.CandidateID, TargetID: e.Command.TargetID, Mode: pin.Mode}
			if pin.AutoApprove {
				a.State = "approved"
				a.Method = "automatic"
			}
			if err = e.SetApproval(a); err != nil {
				return nil, err
			}
			approval = &a
		}
		if approval.State != "approved" {
			if err = e.Exclude(); err != nil {
				return nil, err
			}
			return nil, service.Block("review", "APPROVAL_REQUIRED", "Review the exact package comparison before deployment.")
		}
		if err = e.Exclude("target-system:" + e.Command.TargetID); err != nil {
			return nil, err
		}
		h, err := s.Hosts.Host(ctx, e.Command.TargetID)
		if err != nil {
			return nil, err
		}
		if h.Version != pin.HostVersion || h.MembershipEpoch != pin.HostEpoch {
			return nil, domain.Err("VERSION_CONFLICT", "Target binding or membership changed before activation.", 409)
		}
		current, err := s.makeDiff(ctx, candidate, e.Command.TargetID, "")
		if err != nil {
			return nil, err
		}
		if current.ID != cp.DiffID {
			cp.DiffID = current.ID
			cp.Deployment.DiffID = current.ID
			if err = e.Checkpoint("review", cp, true); err != nil {
				return nil, err
			}
			a := domain.DeploymentApproval{AutoApprove: pin.AutoApprove, State: "pending", DiffID: current.ID, DiffVersion: current.Version, CandidateID: candidate.ID, TargetID: e.Command.TargetID, Mode: pin.Mode}
			if pin.AutoApprove {
				a.State = "approved"
				a.Method = "automatic"
			}
			if err = e.SetApproval(a); err != nil {
				return nil, err
			}
			if !pin.AutoApprove {
				if err = e.Exclude(); err != nil {
					return nil, err
				}
				return nil, service.Block("review", "APPROVAL_REQUIRED", "Running baseline changed; review the replacement comparison.")
			}
		}
		info, err := s.Nix.Info(ctx, candidate.OutputPath)
		if err != nil {
			return nil, err
		}
		if info.NarHash != candidate.NarHash {
			return nil, domain.Err("OUTPUT_CHANGED", "Candidate content changed after approval.", 409)
		}
		if err = s.retainGenerations(ctx, e.Command.TargetID, candidate); err != nil {
			return nil, err
		}
		snap, err := s.targetSystem().Inspect(ctx)
		if err != nil {
			return nil, err
		}
		if snap.Running != current.Baseline.OutputPath {
			replacement, err := s.makeDiff(ctx, candidate, e.Command.TargetID, "")
			if err != nil {
				return nil, err
			}
			cp.DiffID = replacement.ID
			cp.Deployment.DiffID = replacement.ID
			if err = e.Checkpoint("review", cp, true); err != nil {
				return nil, err
			}
			a := domain.DeploymentApproval{AutoApprove: pin.AutoApprove, State: "pending", DiffID: replacement.ID, DiffVersion: replacement.Version, CandidateID: candidate.ID, TargetID: e.Command.TargetID, Mode: pin.Mode}
			if pin.AutoApprove {
				a.State = "approved"
				a.Method = "automatic"
			}
			if err = e.SetApproval(a); err != nil {
				return nil, err
			}
			if err = e.Exclude(); err != nil {
				return nil, err
			}
			if pin.AutoApprove {
				return nil, service.ReconcileLater("BASELINE_CHANGED", "Running output changed during preflight; recheck the replacement comparison.")
			}
			return nil, service.Block("review", "APPROVAL_REQUIRED", "Running output changed during preflight; review the replacement comparison.")
		}
		previous := snap.Generation
		cp.Deployment.PreviousGeneration = &previous
		cp.ProfileIntent = true
		if err = e.Checkpoint("selecting_profile", cp, false); err != nil {
			return nil, err
		}
	}
	// Beyond profile intent, cancellation is unavailable until the persistent
	// activation unit is reconciled. A transport timeout cannot release exclusion.
	if err = e.Exclude("target-system:" + e.Command.TargetID); err != nil {
		return nil, err
	}
	if !cp.ActivationIntent {
		snap, err := s.targetSystem().Select(ctx, candidate.OutputPath)
		if err != nil {
			return nil, service.Block("reconciling", "PROFILE_UNKNOWN", "System profile selection needs reconciliation; no activation was repeated.")
		}
		number := snap.Generation
		cp.Deployment.NewGeneration = &number
		cp.ActivationIntent = true
		cp.Deployment.Activation = "unknown"
		if err = e.Checkpoint("activation_intent", cp, false); err != nil {
			return nil, err
		}
		if err = s.recordDeployment(ctx, cp.Deployment); err != nil {
			return nil, err
		}
		if err = e.MarkEffect("profile_selected", true); err != nil {
			return nil, err
		}
		if err = e.MarkEffect("activation_started", false); err != nil {
			return nil, err
		}
		// This is the only dispatch site. Recovery from this checkpoint solely
		// queries the existing unit, even if its launch acknowledgment was lost.
		_ = s.targetSystem().Start(ctx, e.TaskID, candidate.OutputPath, pin.Mode)
	}
	if err = e.Checkpoint("observing", cp, false); err != nil {
		return nil, err
	}
	{
		state, receiptErr := s.targetSystem().Activation(ctx, e.TaskID)
		// The adapter can prove no activation began: no systemd unit and a
		// missing/planned receipt. A running or unreadable receipt never permits
		// dispatch, including after reboot.
		if receiptErr == nil && state == "not_started" {
			h, err := s.Hosts.Host(ctx, e.Command.TargetID)
			if err != nil {
				return nil, err
			}
			if h.Version != pin.HostVersion || h.MembershipEpoch != pin.HostEpoch {
				return nil, service.ReconcileLater("VERSION_CONFLICT", "Target binding changed before activation could start.")
			}
			if err = s.targetSystem().Start(ctx, e.TaskID, candidate.OutputPath, pin.Mode); err != nil {
				return nil, err
			}
			state, receiptErr = s.targetSystem().Activation(ctx, e.TaskID)
		}
		cp.Deployment.Activation = state
		snap, observeErr := s.targetSystem().Inspect(ctx)
		if observeErr == nil {
			cp.Deployment.ObservedAt = snap.ObservedAt
			cp.Deployment.RunningOutput = snap.Running
			for _, g := range snap.Generations {
				if g.OutputPath == snap.Running {
					number := g.Number
					cp.Deployment.RunningGeneration = &number
				}
			}
			if state == "succeeded" && snap.Profile == candidate.OutputPath {
				number := snap.Generation
				cp.Deployment.BootGeneration = &number
			}
		}
		if receiptErr == nil && state == "failed" {
			cp.Deployment.Observation = "failed"
			_ = s.recordDeployment(ctx, cp.Deployment)
			_ = e.MarkEffect("activation_finished", true)
			return nil, domain.Err("ACTIVATION_FAILED", "Activation failed; inspect the recorded running and profile state before another deployment.", 500)
		}
		if receiptErr == nil && state == "succeeded" && observeErr == nil && snap.Profile == candidate.OutputPath && s.targetSystem().Ready(ctx) && (pin.Mode == "boot" || snap.Running == candidate.OutputPath) {
			cp.Deployment.Observation = "verified"
			if pin.Mode == "boot" {
				cp.Deployment.Observation = "staged"
			}
			if err = s.recordDeployment(ctx, cp.Deployment); err != nil {
				return nil, service.Block("reconciling", "OBSERVATION_UNKNOWN", "Observed deployment could not be durably recorded.")
			}
			if err = s.recordGeneration(ctx, e.Command.TargetID, candidate, snap); err != nil {
				return nil, service.Block("reconciling", "OBSERVATION_UNKNOWN", "Generation history could not be durably recorded.")
			}
			if s.RefreshHost != nil {
				s.RefreshHost()
			}
			if pin.Mode == "switch" && s.VerifyManaged != nil {
				if err = s.VerifyManaged(ctx, candidate, cp.Deployment.ID); err != nil {
					var reason *domain.Error
					if !errors.As(err, &reason) || reason.Code != "VERIFICATION_REQUIRED" {
						return nil, err
					}
				}
			}
			if err = e.MarkEffect("activation_finished", true); err != nil {
				return nil, err
			}
			if err = e.MarkEffect("expected_system_observed", pin.Mode == "switch"); err != nil {
				return nil, err
			}
			return cp.Deployment, nil
		}
		_ = e.SetProgress(map[string]any{"schema_version": 1, "phase": "observing", "freshness": "live", "observed_at": time.Now().UTC(), "activation": state, "observation": "pending", "expected_output": candidate.OutputPath, "running_output": cp.Deployment.RunningOutput})
		return nil, service.ReconcileLater("OBSERVATION_UNKNOWN", "Waiting for the existing activation receipt and expected system; activation will not be repeated.")
	}
}
func (s *Service) retainGenerations(ctx context.Context, host string, candidate domain.Candidate) error {
	snap, err := s.targetSystem().Inspect(ctx)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Join(s.Root, "generations"), 0700); err != nil {
		return err
	}
	// Keep all task-referenced roots; Nix owns its existing generation roots.
	for _, path := range []string{snap.Running, snap.Profile, candidate.OutputPath} {
		if err = s.Nix.Retain(ctx, path, filepath.Join(s.Root, "generations", gitstore.StableID("root_", host+path))); err != nil {
			return err
		}
	}
	for _, g := range snap.Generations {
		if g.OutputPath != snap.Running && g.OutputPath != snap.Profile {
			continue
		}
		info, err := s.Nix.Info(ctx, g.OutputPath)
		if err != nil {
			return err
		}
		g.HostID = host
		g.ID = generationID(host, g.Number, g.OutputPath)
		g.NarHash = info.NarHash
		var old domain.Generation
		rev, err := s.Sources.State.Get(ctx, "generation."+g.ID, &old)
		if err != nil && !absent(err) {
			return err
		}
		g.Source = old.Source
		g.Boot = old.Boot
		if _, err = s.Sources.State.CAS(ctx, "generation."+g.ID, g, rev); err != nil {
			return err
		}
	}
	return nil
}
func (s *Service) recordGeneration(ctx context.Context, host string, candidate domain.Candidate, snap SystemSnapshot) error {
	g := domain.Generation{ID: generationID(host, snap.Generation, candidate.OutputPath), HostID: host, Number: snap.Generation, OutputPath: candidate.OutputPath, NarHash: candidate.NarHash, Source: &candidate.Source, ObservedAt: snap.ObservedAt, Available: true, Running: snap.Running == candidate.OutputPath, Boot: true}
	var old domain.Generation
	rev, err := s.Sources.State.Get(ctx, "generation."+g.ID, &old)
	if err != nil && !absent(err) {
		return err
	}
	_, err = s.Sources.State.CAS(ctx, "generation."+g.ID, g, rev)
	return err
}
