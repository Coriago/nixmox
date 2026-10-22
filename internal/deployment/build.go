// Package deployment coordinates pinned sources, candidates, reviews and target effects.
package deployment

import (
	"context"
	"encoding/json"
	"errors"
	"nixmox/internal/domain"
	"nixmox/internal/gitops"
	"nixmox/internal/gitstore"
	"nixmox/internal/nixstore"
	"nixmox/internal/service"
	"nixmox/internal/storage"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Service struct {
	Sources        *gitops.Service
	Configurations service.ConfigurationReader
	Hosts          service.HostReader
	Identity       func() domain.Identity
	Root           string
	Nix            nixstore.Nix
	System         TargetSystem
	RefreshHost    func()
	VerifyManaged  func(context.Context, domain.Candidate, string) error
}
type Selector struct {
	RepositoryID  string `json:"repository_id,omitempty"`
	Configuration string `json:"configuration,omitempty"`
	RefID         string `json:"ref_id,omitempty"`
	Commit        string `json:"commit,omitempty"`
}
type buildPayload struct {
	HostID    string    `json:"host_id"`
	Source    *Selector `json:"source,omitempty"`
	BuilderID string    `json:"builder_id,omitempty"`
}
type pinnedBuild struct {
	Source      domain.Source `json:"source"`
	HostVersion int64         `json:"host_version"`
	HostEpoch   int           `json:"host_epoch"`
	System      string        `json:"system"`
	BuilderID   string        `json:"builder_id"`
	At          time.Time     `json:"at"`
}

func (s *Service) Candidate(ctx context.Context, id string) (domain.Candidate, error) {
	var candidate domain.Candidate
	_, err := s.Sources.State.Get(ctx, "candidate."+id, &candidate)
	return candidate, err
}
func (s *Service) Operations() []service.Operation {
	ops := []service.Operation{}
	for _, kind := range []string{"deployment.preview", "deployment.build"} {
		op := service.Operation{Kind: kind, Scope: "cluster", RunOnCoordinator: true, Timeout: 2 * time.Hour, SafeRetry: true}
		op.Validate = func(c domain.Command) error {
			var p buildPayload
			if domain.StrictJSON(c.Payload, &p) != nil || p.HostID != c.TargetID || len(c.ExpectedVersions) != 1 || c.ExpectedVersions["host:"+c.TargetID] < 1 {
				return domain.Err("INVALID_PAYLOAD", "Deployment requires host_id and its reviewed version.", 422)
			}
			if c.Type == "deployment.preview" && p.BuilderID != "" {
				return domain.Err("INVALID_PAYLOAD", "Preview does not select a builder.", 422)
			}
			if p.Source != nil && p.Source.RefID != "" && p.Source.Commit != "" {
				return domain.Err("INVALID_SOURCE", "Ref and commit are mutually exclusive.", 422)
			}
			return nil
		}
		op.Resolve = s.resolveBuild
		op.Run = s.runBuild
		op.Reconcile = s.runBuild
		ops = append(ops, op)
	}
	return append(ops, s.workflowOperations()...)
}
func (s *Service) resolveBuild(ctx context.Context, c domain.Command) (json.RawMessage, error) {
	var p buildPayload
	_ = json.Unmarshal(c.Payload, &p)
	h, err := s.Hosts.Host(ctx, c.TargetID)
	if err != nil {
		return nil, err
	}
	if h.Version != c.ExpectedVersions["host:"+h.ID] {
		return nil, domain.Err("VERSION_CONFLICT", "Host binding changed after review.", 409)
	}
	selector := Selector{}
	if p.Source != nil {
		selector = *p.Source
	}
	if selector.RepositoryID == "" && h.Configuration != nil {
		selector.RepositoryID = h.Configuration.RepositoryID
	}
	if selector.Configuration == "" && h.Configuration != nil && selector.RepositoryID == h.Configuration.RepositoryID {
		selector.Configuration = h.Configuration.Name
		if selector.RefID == "" && selector.Commit == "" {
			selector.RefID = h.Configuration.RefID
		}
	}
	if selector.RepositoryID == "" || selector.Configuration == "" {
		return nil, domain.Err("CONFIGURATION_REQUIRED", "Select a repository/configuration or assign one to this host.", 422)
	}
	page, err := s.Configurations.Configurations(ctx, selector.RepositoryID, "", "", "500", "")
	if err != nil {
		return nil, err
	}
	var cfg domain.Configuration
	for _, v := range page.Items {
		if v.ID == selector.Configuration || v.Name == selector.Configuration {
			cfg = v
			break
		}
	}
	if cfg.ID == "" {
		return nil, domain.Err("NOT_FOUND", "Selected configuration is unavailable.", 404)
	}
	if selector.RefID == "" && selector.Commit == "" {
		selector.RefID = cfg.RefID
	}
	r, ref, commit, err := s.Sources.Source(ctx, selector.RepositoryID, selector.RefID, selector.Commit)
	if err != nil {
		return nil, err
	}
	if selector.Commit == "" {
		for _, v := range r.Refs {
			if v.ID == ref && v.Kind == "upstream" {
				commit, err = s.Sources.Git.Fetch(ctx, r.ID, r.Remote.URL, r.Upstream.Branch, r.Remote.CredentialRef)
				if err != nil {
					return nil, err
				}
				break
			}
		}
	}
	// Re-read the descriptor at the pinned revision; a repository change may
	// move its exported attribute without changing the configuration's name.
	exact, err := s.Configurations.Configurations(ctx, r.ID, "", commit, "500", "")
	if err != nil {
		return nil, err
	}
	found := false
	for _, v := range exact.Items {
		if v.Name == cfg.Name {
			cfg = v
			found = true
			break
		}
	}
	if !found {
		return nil, domain.Err("CONFIGURATION_UNAVAILABLE", "Pinned revision does not export the selected configuration.", 422)
	}
	pin := pinnedBuild{Source: domain.Source{RepositoryID: r.ID, Configuration: cfg.Name, RefID: ref, Commit: commit, FlakeDirectory: cfg.FlakeDirectory, FlakeAttribute: cfg.FlakeAttribute}, HostVersion: h.Version, HostEpoch: h.MembershipEpoch, System: h.System, BuilderID: s.Identity().NodeID, At: time.Now().UTC()}
	if c.Type == "deployment.build" {
		if p.BuilderID != "" && p.BuilderID != pin.BuilderID {
			return nil, domain.Err("BUILDER_UNAVAILABLE", "Selected remote builder is not available through the cache gateway.", 503)
		}
		builder, err := s.Hosts.Host(ctx, pin.BuilderID)
		if err != nil {
			return nil, err
		}
		if builder.Reachability != "online" || !builder.Capabilities.Build {
			return nil, domain.Err("BUILDER_UNAVAILABLE", "The coordinator has no available local Nix builder.", 503)
		}
		if h.ID != builder.ID && !builder.Capabilities.CacheServe {
			return nil, domain.Err("CACHE_UNAVAILABLE", "Cross-host builds require a ready authenticated cache gateway.", 503)
		}
		if h.System != "" && h.System != builder.System {
			return nil, domain.Err("PLATFORM_MISMATCH", "Builder cannot realize the target platform.", 422)
		}
	}
	return json.Marshal(pin)
}
func (s *Service) runBuild(ctx context.Context, e *service.Execution) (any, error) {
	var pin pinnedBuild
	if domain.StrictJSON(e.Resolved, &pin) != nil {
		return nil, domain.Err("INVALID_STATE", "Build source resolution is invalid.", 409)
	}
	if pin.BuilderID != s.Identity().NodeID {
		return nil, domain.Err("BUILDER_UNAVAILABLE", "Pinned builder identity differs from the executor.", 503)
	}
	id := gitstore.StableID("cand_", e.Command.ID)
	if e.Command.Type != "deployment.preview" {
		candidate, err := s.Candidate(ctx, id)
		if err == nil {
			info, err := s.Nix.Info(ctx, candidate.OutputPath)
			if err != nil {
				return nil, err
			}
			if info.NarHash != candidate.NarHash {
				return nil, domain.Err("OUTPUT_CHANGED", "Pinned output content differs from candidate evidence.", 409)
			}
			return candidate, nil
		}
		var failure *domain.Error
		if !errors.As(err, &failure) || failure.Code != "NOT_FOUND" {
			return nil, err
		}
	}
	if err := e.SetSource(pin.Source, pin.BuilderID); err != nil {
		return nil, err
	}
	repo, err := s.Sources.Repository(ctx, pin.Source.RepositoryID)
	if err != nil {
		return nil, err
	}
	if err = s.Sources.Materialize(ctx, repo, pin.Source.Commit); err != nil {
		return nil, err
	}
	dir, err := s.Sources.Git.Worktree(ctx, repo.ID, pin.Source.Commit, id)
	if err != nil {
		return nil, err
	}
	task, err := e.Snapshot()
	if err != nil {
		return nil, err
	}
	var evaluation nixstore.Evaluation
	if task.Phase == "evaluated" || task.Phase == "building" {
		if domain.StrictJSON(task.Checkpoint, &evaluation) != nil {
			return nil, domain.Err("INVALID_STATE", "Build checkpoint is invalid.", 409)
		}
	} else {
		evaluation, err = s.Nix.EvaluateGit(ctx, dir, pin.Source.FlakeDirectory, pin.Source.Commit, pin.Source.FlakeAttribute)
		if err != nil {
			return nil, err
		}
		if pin.System != "" && evaluation.System != pin.System {
			return nil, domain.Err("PLATFORM_MISMATCH", "Configuration evaluates to another target platform.", 422)
		}
		if err = e.Checkpoint("evaluated", evaluation, true); err != nil {
			return nil, err
		}
	}
	if e.Command.Type == "deployment.preview" {
		return map[string]any{"source": pin.Source, "evaluation": evaluation, "package_diff": map[string]string{"state": "unavailable", "reason": "Package diff requires a built candidate."}}, nil
	}
	if err = e.Checkpoint("building", evaluation, true); err != nil {
		return nil, err
	}
	tracker := nixstore.NewTracker(e.TaskID, pin.BuilderID)
	var progressMu sync.Mutex
	publish := func() {
		progressMu.Lock()
		progress, activities := tracker.Snapshot()
		progressMu.Unlock()
		if s.saveProgress(e.TaskID, progress, activities) == nil {
			_ = e.SetProgress(progress)
		}
	}
	progressContext, stopProgress := context.WithCancel(ctx)
	progressDone := make(chan struct{})
	go func() {
		defer close(progressDone)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		publish()
		for {
			select {
			case <-progressContext.Done():
				return
			case <-ticker.C:
				publish()
			}
		}
	}()
	output, err := s.Nix.Build(ctx, evaluation, filepath.Join(s.Root, "candidates", id), func(line []byte) { progressMu.Lock(); tracker.Observe(line); progressMu.Unlock() })
	stopProgress()
	<-progressDone
	tracker.Finish(err == nil)
	publish()
	if err != nil {
		return nil, err
	}
	candidate := domain.Candidate{SchemaVersion: 1, ID: id, Version: 1, Source: pin.Source, BuilderID: pin.BuilderID, System: evaluation.System, State: "built", OutputPath: output.Path, NarHash: output.NarHash, InputLockDigest: evaluation.InputLockDigest, DerivationPath: evaluation.Derivation, TaskID: e.TaskID, CreatedAt: pin.At}
	if comparison, compareErr := s.makeDiff(ctx, candidate, e.Command.TargetID, ""); compareErr == nil {
		candidate.Comparison = &domain.CandidateComparison{DiffID: comparison.ID, HostID: e.Command.TargetID}
	}
	if _, err = s.Sources.State.CAS(ctx, "candidate."+id, candidate, 0); err != nil {
		return nil, err
	}
	return candidate, nil
}

type progressRecord struct {
	Progress   nixstore.Progress   `json:"progress"`
	Activities []nixstore.Activity `json:"activities"`
}

func (s *Service) saveProgress(task string, progress nixstore.Progress, activities []nixstore.Activity) error {
	if !domain.SafeID.MatchString(task) {
		return domain.Err("INVALID_ID", "Invalid task ID.", 422)
	}
	if err := os.MkdirAll(filepath.Join(s.Root, "progress"), 0700); err != nil {
		return err
	}
	return (&storage.Store{Root: s.Root}).Write("progress/"+task+".json", progressRecord{progress, activities})
}
func (s *Service) Activities(ctx context.Context, task, limit, cursor string) (any, error) {
	if !domain.SafeID.MatchString(task) {
		return nil, domain.Err("INVALID_ID", "Invalid task ID.", 422)
	}
	var record progressRecord
	if err := (&storage.Store{Root: s.Root}).Read("progress/"+task+".json", &record); err != nil {
		return nil, domain.Err("OWNER_UNAVAILABLE", "Build activities are unavailable on this console; use the task owner.", 503)
	}
	return domain.Paginate(record.Activities, struct {
		Task    string
		Version int64
	}{task, record.Progress.Version}, limit, cursor, func(a nixstore.Activity) string { return a.ID })
}
