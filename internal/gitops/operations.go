package gitops

import (
	"context"
	"encoding/json"
	"regexp"
	"time"

	"nixmox/internal/config"
	"nixmox/internal/domain"
	"nixmox/internal/gitstore"
	"nixmox/internal/service"
)

type repositoryPayload struct {
	Name           string  `json:"name,omitempty"`
	RemoteURL      string  `json:"remote_url,omitempty"`
	UpstreamBranch string  `json:"upstream_branch,omitempty"`
	CredentialRef  *string `json:"credential_ref,omitempty"`
	RepositoryID   string  `json:"repository_id,omitempty"`
	URL            string  `json:"url,omitempty"`
}
type repositoryResolution struct {
	RepositoryID string    `json:"repository_id"`
	Version      int64     `json:"version"`
	At           time.Time `json:"at"`
}
type fetched struct {
	Commit string    `json:"commit"`
	At     time.Time `json:"at"`
}

var NamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

func (s *Service) Operations() []service.Operation {
	ops := []service.Operation{}
	for _, kind := range []string{"repository.import", "repository.fetch", "repository.remote.set", "repository.credential.set"} {
		op := service.Operation{Kind: kind, Scope: "cluster", Timeout: 3 * time.Minute, SafeRetry: true}
		op.Validate = func(c domain.Command) error {
			var p repositoryPayload
			if domain.StrictJSON(c.Payload, &p) != nil {
				return domain.Err("INVALID_PAYLOAD", "Invalid repository fields.", 422)
			}
			if p.CredentialRef != nil && !domain.SafeID.MatchString(*p.CredentialRef) {
				return domain.Err("INVALID_PAYLOAD", "Invalid credential reference.", 422)
			}
			switch c.Type {
			case "repository.import":
				if !NamePattern.MatchString(p.Name) || config.ValidateRemote(p.RemoteURL) != nil || gitstore.ValidateBranch(p.UpstreamBranch) != nil || p.RepositoryID != "" || p.URL != "" || len(c.ExpectedVersions) != 0 {
					return domain.Err("INVALID_PAYLOAD", "Import requires name, remote_url and upstream_branch.", 422)
				}
			default:
				if !domain.SafeID.MatchString(p.RepositoryID) || p.Name != "" || p.RemoteURL != "" {
					return domain.Err("INVALID_PAYLOAD", "A repository_id is required.", 422)
				}
				if c.Type == "repository.fetch" {
					if len(c.ExpectedVersions) != 0 || p.URL != "" || p.UpstreamBranch != "" || p.CredentialRef != nil {
						return domain.Err("INVALID_PAYLOAD", "Fetch accepts only repository_id.", 422)
					}
				} else {
					if len(c.ExpectedVersions) != 1 || c.ExpectedVersions["repository:"+p.RepositoryID] < 1 {
						return domain.Err("VERSION_REQUIRED", "A reviewed repository version is required.", 422)
					}
					if c.Type == "repository.remote.set" && (config.ValidateRemote(p.URL) != nil || p.CredentialRef != nil || (p.UpstreamBranch != "" && gitstore.ValidateBranch(p.UpstreamBranch) != nil)) {
						return domain.Err("INVALID_PAYLOAD", "Invalid repository remote settings.", 422)
					}
					if c.Type == "repository.credential.set" && (p.URL != "" || p.UpstreamBranch != "") {
						return domain.Err("INVALID_PAYLOAD", "Credential update accepts only repository_id and credential_ref.", 422)
					}
				}
			}
			return nil
		}
		op.Resolve = func(ctx context.Context, c domain.Command) (json.RawMessage, error) {
			var p repositoryPayload
			_ = json.Unmarshal(c.Payload, &p)
			v := repositoryResolution{RepositoryID: p.RepositoryID, At: time.Now().UTC()}
			if c.Type == "repository.import" {
				v.RepositoryID = gitstore.StableID("r_", p.Name)
				_, err := s.Repository(ctx, v.RepositoryID)
				if err == nil {
					return nil, domain.Err("NAME_CONFLICT", "Repository name is already registered.", 409)
				}
				if !missing(err) {
					return nil, err
				}
			} else {
				r, err := s.Repository(ctx, p.RepositoryID)
				if err != nil {
					return nil, err
				}
				v.Version = r.Version
				if c.Type != "repository.fetch" && c.ExpectedVersions["repository:"+r.ID] != r.Version {
					return nil, domain.Err("VERSION_CONFLICT", "Repository changed after review.", 409)
				}
			}
			return encoded(v), nil
		}
		op.Exclusions = func(c domain.Command, b json.RawMessage) ([]string, error) {
			var v repositoryResolution
			if err := domain.StrictJSON(b, &v); err != nil {
				return nil, err
			}
			return []string{"repository:" + v.RepositoryID}, nil
		}
		op.Run = s.runRepository
		op.Reconcile = s.runRepository
		ops = append(ops, op)
	}
	return ops
}
func (s *Service) runRepository(ctx context.Context, e *service.Execution) (any, error) {
	var p repositoryPayload
	var v repositoryResolution
	if domain.StrictJSON(e.Command.Payload, &p) != nil || domain.StrictJSON(e.Resolved, &v) != nil {
		return nil, domain.Err("INVALID_STATE", "Repository command resolution is invalid.", 409)
	}
	var r domain.Repository
	rev, err := s.State.Get(ctx, "repository."+v.RepositoryID, &r)
	if err == nil {
		if accepted, ok := r.AcceptedCommands[e.Command.ID]; ok {
			return accepted, nil
		}
	}
	if e.Command.Type == "repository.import" {
		if err == nil {
			return nil, domain.Err("NAME_CONFLICT", "Repository name is already registered.", 409)
		}
		if !missing(err) {
			return nil, err
		}
		r = domain.Repository{SchemaVersion: 1, ID: v.RepositoryID, Name: p.Name, Remote: domain.RepositoryRemote{URL: p.RemoteURL, CredentialRef: p.CredentialRef}, Upstream: domain.RepositoryUpstream{Branch: p.UpstreamBranch}, Refs: []domain.SourceRef{}, Configurations: []domain.Configuration{}, Artifacts: []string{}, AcceptedCommands: map[string]domain.SourceAcceptance{}}
		rev = 0
	} else {
		if err != nil {
			return nil, err
		}
		if r.Version != v.Version {
			return nil, domain.Err("VERSION_CONFLICT", "Repository changed since command admission.", 409)
		}
	}
	switch e.Command.Type {
	case "repository.import", "repository.fetch":
		if r.Remote.CredentialRef != nil {
			return nil, service.Block("credentials", "CREDENTIAL_UNAVAILABLE", "App-managed read credential is unavailable on this fetching node.")
		}
		task, err := e.Snapshot()
		if err != nil {
			return nil, err
		}
		var pin fetched
		if task.Phase == "source_fetched" {
			if domain.StrictJSON(task.Checkpoint, &pin) != nil {
				return nil, domain.Err("INVALID_STATE", "Fetch checkpoint is invalid.", 409)
			}
		} else {
			pin.Commit, err = s.Git.Fetch(ctx, r.ID, r.Remote.URL, r.Upstream.Branch)
			if err != nil {
				return nil, err
			}
			pin.At = time.Now().UTC()
			if err = e.Checkpoint("source_fetched", pin, true); err != nil {
				return nil, err
			}
		}
		r.Upstream.Commit = &pin.Commit
		r.Upstream.FetchedAt = &pin.At
		ref := domain.SourceRef{ID: gitstore.StableID("ref_", r.ID+"/upstream"), RepositoryID: r.ID, Kind: "upstream", Name: r.Upstream.Branch, Head: pin.Commit, Publication: domain.SourcePublication{State: "upstream"}}
		found := false
		for i, prior := range r.Refs {
			if prior.Kind == "upstream" {
				r.Refs[i] = ref
				found = true
			} else {
				if err = s.Materialize(ctx, r, prior.Head); err != nil {
					return nil, err
				}
				r.Refs[i].Ahead, r.Refs[i].Behind, err = s.Git.Counts(ctx, r.ID, pin.Commit, prior.Head)
				if err != nil {
					return nil, err
				}
			}
		}
		if !found {
			r.Refs = append(r.Refs, ref)
		}
	case "repository.remote.set":
		r.Remote.URL = p.URL
		if p.UpstreamBranch != "" {
			r.Upstream.Branch = p.UpstreamBranch
		}
		r.Upstream.FetchedAt = nil
	case "repository.credential.set":
		r.Remote.CredentialRef = p.CredentialRef
	}
	accepted := domain.SourceAcceptance{}
	if r.Upstream.Commit != nil {
		accepted.Commit = *r.Upstream.Commit
	}
	accepted.RefID = gitstore.StableID("ref_", r.ID+"/upstream")
	if r.AcceptedCommands == nil {
		r.AcceptedCommands = map[string]domain.SourceAcceptance{}
	}
	r.AcceptedCommands[e.Command.ID] = accepted
	r.Version++
	if _, err = s.State.CAS(ctx, "repository."+r.ID, r, rev); err != nil {
		return nil, err
	}
	return struct {
		RepositoryID string `json:"repository_id"`
		Version      int64  `json:"version"`
		domain.SourceAcceptance
	}{r.ID, r.Version, accepted}, nil
}
