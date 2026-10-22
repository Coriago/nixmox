package deployment

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"nixmox/internal/domain"
	"nixmox/internal/gitstore"
	"nixmox/internal/service"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func (s *Service) targetSystem() TargetSystem {
	if s.System != nil {
		return s.System
	}
	return LocalSystem{}
}
func absent(err error) bool { var d *domain.Error; return errors.As(err, &d) && d.Code == "NOT_FOUND" }
func (s *Service) localTarget(id string) error {
	if id != s.Identity().NodeID {
		return domain.Err("TARGET_UNAVAILABLE", "Target effects require an authenticated target executor and ready cache transfer.", 503)
	}
	return nil
}

type diffRecord struct {
	Diff   domain.PackageDiff `json:"diff"`
	Chunks int                `json:"chunks"`
}

func (s *Service) readDiff(ctx context.Context, id string) (diffRecord, error) {
	var r diffRecord
	_, err := s.Sources.State.Get(ctx, "diff."+id, &r)
	return r, err
}
func (s *Service) Diff(ctx context.Context, id string) (domain.PackageDiff, error) {
	r, err := s.readDiff(ctx, id)
	if err != nil {
		return r.Diff, err
	}
	page, err := s.DiffChanges(ctx, id, "", "", "", "")
	if err != nil {
		return r.Diff, err
	}
	r.Diff.Changes = page.Items
	r.Diff.NextCursor = page.NextCursor
	if r.Diff.Baseline.Kind == "running" {
		h, err := s.Hosts.Host(ctx, r.Diff.HostID)
		if err != nil || h.RunningOutput == nil || *h.RunningOutput != r.Diff.Baseline.OutputPath || h.Reachability != "online" {
			r.Diff.Stale = true
		}
	}
	return r.Diff, nil
}
func (s *Service) DiffChanges(ctx context.Context, id, kind, q, limit, cursor string) (domain.Page[domain.PackageChange], error) {
	page := domain.Page[domain.PackageChange]{}
	if len(q) > 512 || kind != "" && kind != "added" && kind != "removed" && kind != "version_changed" && kind != "output_changed" && kind != "ambiguous" {
		return page, domain.Err("INVALID_QUERY", "Invalid package filter.", 400)
	}
	r, err := s.readDiff(ctx, id)
	if err != nil {
		return page, err
	}
	if r.Chunks < 0 || r.Chunks > 1250 {
		return page, domain.Err("INVALID_STATE", "Comparison chunk bound exceeded.", 409)
	}
	rows := []domain.PackageChange{}
	for i := 0; i < r.Chunks; i++ {
		var chunk []domain.PackageChange
		if _, err = s.Sources.State.Get(ctx, fmt.Sprintf("diff_rows.%s_%04d", id, i), &chunk); err != nil {
			return page, err
		}
		for _, row := range chunk {
			if matchesChange(row, kind, q) {
				rows = append(rows, row)
			}
		}
		if len(rows) > 40000 {
			return page, domain.Err("STORAGE_LIMIT", "Comparison exceeds its row bound.", 507)
		}
	}
	return domain.Paginate(rows, []any{id, r.Diff.Version, kind, q}, limit, cursor, func(c domain.PackageChange) string { return c.ID })
}
func (s *Service) Generations(ctx context.Context, host, limit, cursor string) (domain.Page[domain.Generation], error) {
	page := domain.Page[domain.Generation]{}
	if err := s.localTarget(host); err != nil {
		return page, err
	}
	snap, err := s.targetSystem().Inspect(ctx)
	if err != nil {
		return page, err
	}
	rows := snap.Generations
	for i := range rows {
		g := &rows[i]
		g.HostID = host
		g.ID = generationID(host, g.Number, g.OutputPath)
		var old domain.Generation
		_, err := s.Sources.State.Get(ctx, "generation."+g.ID, &old)
		if err == nil {
			g.Source = old.Source
			g.NarHash = old.NarHash
			g.Boot = old.Boot && snap.Profile == g.OutputPath
		} else if !absent(err) {
			return page, err
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Number > rows[j].Number })
	return domain.Paginate(rows, struct{ Host, Profile, Running string }{host, snap.Profile, snap.Running}, limit, cursor, func(g domain.Generation) string { return g.ID })
}
func generationID(host string, number int, output string) string {
	return gitstore.StableID("gen_", fmt.Sprintf("%s:%d:%s", host, number, output))
}
func (s *Service) lookupGeneration(ctx context.Context, host, id string) (domain.Generation, error) {
	var g domain.Generation
	_, err := s.Sources.State.Get(ctx, "generation."+id, &g)
	if err == nil {
		if g.HostID != host {
			return g, domain.Err("GENERATION_INVALID", "Generation belongs to another host.", 422)
		}
		return g, nil
	}
	if !absent(err) {
		return g, err
	}
	if err = s.localTarget(host); err != nil {
		return g, err
	}
	snapshot, err := s.targetSystem().Inspect(ctx)
	if err != nil {
		return g, err
	}
	for _, v := range snapshot.Generations {
		if generationID(host, v.Number, v.OutputPath) != id {
			continue
		}
		info, err := s.Nix.Info(ctx, v.OutputPath)
		if err != nil {
			return g, err
		}
		v.ID = id
		v.HostID = host
		v.NarHash = info.NarHash
		return v, nil
	}
	return g, domain.Err("OUTPUT_UNAVAILABLE", "Recorded generation is no longer available.", 503)
}
func (s *Service) makeDiff(ctx context.Context, c domain.Candidate, host, baselineID string) (domain.PackageDiff, error) {
	var d domain.PackageDiff
	if err := s.localTarget(host); err != nil {
		return d, err
	}
	snap, err := s.targetSystem().Inspect(ctx)
	if err != nil {
		return d, err
	}
	baseline := domain.DiffBaseline{Kind: "running", OutputPath: snap.Running, ObservedAt: snap.ObservedAt}
	if baselineID != "" {
		g, lookupErr := s.lookupGeneration(ctx, host, baselineID)
		if lookupErr != nil {
			return d, lookupErr
		}
		if g.HostID != host {
			return d, domain.Err("BASELINE_INVALID", "Generation belongs to another host.", 422)
		}
		baseline.Kind = "generation"
		baseline.OutputPath = g.OutputPath
		baseline.GenerationID = g.ID
	} else {
		for _, g := range snap.Generations {
			if g.OutputPath == snap.Running {
				baseline.GenerationID = generationID(host, g.Number, g.OutputPath)
				break
			}
		}
	}
	old, err := s.Nix.Info(ctx, baseline.OutputPath)
	if err != nil {
		return d, err
	}
	baseline.NarHash = old.NarHash
	current, err := s.Nix.Info(ctx, c.OutputPath)
	if err != nil {
		return d, err
	}
	if current.NarHash != c.NarHash {
		return d, domain.Err("OUTPUT_CHANGED", "Candidate output differs from verified evidence.", 409)
	}
	id := gitstore.StableID("diff_", strings.Join([]string{"1", host, c.ID, c.OutputPath, c.NarHash, baseline.OutputPath, baseline.NarHash, baseline.Kind, baseline.GenerationID}, "\n"))
	stored, err := s.readDiff(ctx, id)
	if err == nil {
		return stored.Diff, nil
	}
	if !absent(err) {
		return d, err
	}
	if err = os.MkdirAll(filepath.Join(s.Root, "reviews"), 0700); err != nil {
		return d, err
	}
	for suffix, output := range map[string]string{"before": baseline.OutputPath, "after": c.OutputPath} {
		if err = s.Nix.Retain(ctx, output, filepath.Join(s.Root, "reviews", id+"-"+suffix)); err != nil {
			return d, err
		}
	}
	before, err := s.Nix.Closure(ctx, baseline.OutputPath)
	if err != nil {
		return d, err
	}
	after, err := s.Nix.Closure(ctx, c.OutputPath)
	if err != nil {
		return d, err
	}
	changes, summary, size := compareClosures(before, after)
	d = domain.PackageDiff{SchemaVersion: 1, ID: id, Version: 1, HostID: host, CandidateID: c.ID, Baseline: baseline, CandidateOutput: c.OutputPath, CandidateNarHash: c.NarHash, Scope: "system_closure", State: "ready", Summary: summary, ClosureSize: size, Changes: []domain.PackageChange{}}
	chunks := 0
	for start := 0; start < len(changes); start += 32 {
		chunk := changes[start:min(start+32, len(changes))]
		key := fmt.Sprintf("diff_rows.%s_%04d", id, chunks)
		if err = s.immutable(ctx, key, chunk); err != nil {
			return d, err
		}
		chunks++
	}
	if err = s.immutable(ctx, "diff."+id, diffRecord{d, chunks}); err != nil {
		return d, err
	}
	return d, nil
}
func (s *Service) immutable(ctx context.Context, key string, value any) error {
	var existing json.RawMessage
	_, err := s.Sources.State.Get(ctx, key, &existing)
	if err == nil {
		a, _ := json.Marshal(value)
		var av, bv any
		ad := json.NewDecoder(bytes.NewReader(a))
		ad.UseNumber()
		_ = ad.Decode(&av)
		bd := json.NewDecoder(bytes.NewReader(existing))
		bd.UseNumber()
		_ = bd.Decode(&bv)
		a, _ = json.Marshal(av)
		b, _ := json.Marshal(bv)
		if !bytes.Equal(a, b) {
			return domain.Err("VERSION_CONFLICT", "Immutable record differs from stored evidence.", 409)
		}
		return nil
	}
	if !absent(err) {
		return err
	}
	_, err = s.Sources.State.CAS(ctx, key, value, 0)
	return err
}

type diffPayload struct {
	HostID      string `json:"host_id"`
	CandidateID string `json:"candidate_id"`
	BaselineID  string `json:"baseline_generation_id,omitempty"`
}

func (s *Service) runDiff(ctx context.Context, e *service.Execution) (any, error) {
	var p diffPayload
	_ = json.Unmarshal(e.Command.Payload, &p)
	c, err := s.Candidate(ctx, p.CandidateID)
	if err != nil {
		return nil, err
	}
	return s.makeDiff(ctx, c, e.Command.TargetID, p.BaselineID)
}
