package service

import (
	"context"
	"encoding/json"

	"nixmox/internal/domain"
	"nixmox/internal/engine"
)

// candidateBuild is executor evidence for the forthcoming deployment phases.
// The root is owner-local; public candidate reads expose Candidate only.
type candidateBuild struct {
	Candidate domain.Candidate `json:"candidate"`
	Root      string           `json:"root"`
}

// buildCandidate starts the deployment implementation without exposing activation.
// Admission must first persist source, system and builder under the task receipt.
// Callers retain the returned root through diff/approval/history and publish the
// candidate only after the task checkpoint is durable. No recipe generation runs.
func (c *ClusterNode) buildCandidate(ctx context.Context, e engine.Execution, source domain.Source, system string) (candidateBuild, error) {
	var result candidateBuild
	if source.Recipe != nil {
		return result, domain.Unavailable("recipe realization")
	}
	candidateID := sourceID("candidate", string(e.Task().ID))
	intent, err := json.Marshal(struct {
		Source      domain.Source `json:"source"`
		System      string        `json:"system"`
		CandidateID domain.ID     `json:"candidate_id"`
	}{source, system, candidateID})
	if err != nil {
		return result, err
	}
	if err = e.Intent(ctx, domain.Checkpoint{Phase: "build", Evidence: intent}); err != nil {
		return result, err
	}
	c.repositoryMu.Lock()
	checkout, err := c.git.Materialize(ctx, source)
	c.repositoryMu.Unlock()
	if err != nil {
		return result, err
	}
	defer func() { c.repositoryMu.Lock(); defer c.repositoryMu.Unlock(); _ = checkout.Close() }()
	nix := c.nix
	nix.Runner = e.Runner(nix.Runner)
	built, err := nix.Build(ctx, engine.BuildRequest{Source: source, Worktree: checkout.Path(), System: system}, candidateID)
	result.Root = built.Root
	if err != nil {
		return result, err
	}
	result.Candidate = domain.Candidate{Record: domain.Record{SchemaVersion: 1, ID: candidateID, Version: 1}, Source: source, BuilderID: e.Task().OwnerNodeID, System: system, State: "built", OutputPath: &built.Output.StorePath, NARHash: &built.Output.NARHash, InputLockDigest: built.InputLockDigest}
	data, err := json.Marshal(result)
	if err != nil {
		return result, err
	}
	return result, e.Observed(ctx, domain.Checkpoint{Phase: "build", Evidence: data})
}
