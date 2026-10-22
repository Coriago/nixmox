package deployment

import (
	"context"
	"encoding/json"
	"nixmox/internal/domain"
	"nixmox/internal/gitstore"
	"nixmox/internal/service"
	"time"
)

type adoptPayload struct {
	HostID      string `json:"host_id"`
	CandidateID string `json:"candidate_id"`
	ConfirmHost string `json:"confirm_host"`
}

func (s *Service) adoptOperation() service.Operation {
	return service.Operation{Kind: "host.adopt", Scope: "cluster", RunOnCoordinator: true, SafeRetry: true, Timeout: time.Minute,
		Validate: func(c domain.Command) error {
			var p adoptPayload
			if domain.StrictJSON(c.Payload, &p) != nil || p.HostID != c.TargetID || !domain.SafeID.MatchString(p.CandidateID) || p.ConfirmHost == "" || len(c.ExpectedVersions) != 1 || c.ExpectedVersions["host:"+c.TargetID] < 1 {
				return domain.Err("INVALID_PAYLOAD", "Adoption requires a candidate, host confirmation and reviewed host version.", 422)
			}
			return nil
		},
		Resolve: func(ctx context.Context, c domain.Command) (json.RawMessage, error) {
			var p adoptPayload
			_ = json.Unmarshal(c.Payload, &p)
			if err := s.localTarget(c.TargetID); err != nil {
				return nil, err
			}
			h, err := s.Hosts.Host(ctx, c.TargetID)
			if err != nil {
				return nil, err
			}
			if h.Version != c.ExpectedVersions["host:"+h.ID] {
				return nil, domain.Err("VERSION_CONFLICT", "Host changed after review.", 409)
			}
			if p.ConfirmHost != h.ID && p.ConfirmHost != h.Name {
				return nil, domain.Err("CONFIRMATION_REQUIRED", "Confirmation does not match the selected host.", 409)
			}
			candidate, err := s.Candidate(ctx, p.CandidateID)
			if err != nil {
				return nil, err
			}
			return json.Marshal(candidate)
		},
		Run: s.runAdopt, Reconcile: s.runAdopt,
	}
}
func (s *Service) runAdopt(ctx context.Context, e *service.Execution) (any, error) {
	var candidate domain.Candidate
	if domain.StrictJSON(e.Resolved, &candidate) != nil {
		return nil, domain.Err("INVALID_STATE", "Adoption lost its candidate identity.", 409)
	}
	if err := e.Exclude("target-system:" + e.Command.TargetID); err != nil {
		return nil, err
	}
	id := gitstore.StableID("adopt_", e.Command.ID)
	h, err := s.Hosts.Host(ctx, e.Command.TargetID)
	if err != nil {
		return nil, err
	}
	if h.Version != e.Command.ExpectedVersions["host:"+h.ID] {
		var previous domain.ManagedEvidence
		_, readErr := s.Sources.State.Get(ctx, "managed."+h.ID, &previous)
		if readErr != nil || previous.DeploymentID != id || previous.CandidateID != candidate.ID || previous.Epoch != h.MembershipEpoch {
			return nil, domain.Err("VERSION_CONFLICT", "Host changed before running-system verification.", 409)
		}
	}
	if s.VerifyManaged == nil {
		return nil, domain.Err("VERIFICATION_REQUIRED", "Permanent agent verification is unavailable.", 503)
	}
	snapshot, err := s.targetSystem().Inspect(ctx)
	if err != nil {
		return nil, err
	}
	if snapshot.Running != candidate.OutputPath {
		if err = e.Exclude(); err != nil {
			return nil, err
		}
		return nil, service.Block("verification", "OUTPUT_MISMATCH", "Running system differs from the pinned candidate; adoption performs no activation.")
	}
	output, err := s.Nix.Info(ctx, snapshot.Running)
	if err != nil {
		return nil, err
	}
	if output.NarHash != candidate.NarHash {
		return nil, domain.Err("OUTPUT_CHANGED", "Running output content differs from the candidate.", 409)
	}
	if !s.targetSystem().Ready(ctx) {
		return nil, domain.Err("OBSERVATION_UNAVAILABLE", "The authenticated management service is not responsive.", 503)
	}
	if err = e.SetSource(candidate.Source, candidate.BuilderID); err != nil {
		return nil, err
	}
	if err = s.VerifyManaged(ctx, candidate, id); err != nil {
		return nil, err
	}
	if s.RefreshHost != nil {
		s.RefreshHost()
	}
	return map[string]any{"host_id": e.Command.TargetID, "candidate_id": candidate.ID, "output_path": candidate.OutputPath, "verified": true, "activation": "not_requested"}, nil
}
