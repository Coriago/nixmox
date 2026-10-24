package nix

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"

	"nixmox/internal/domain"
	"nixmox/internal/engine"
	"nixmox/internal/engine/internal/shared"
	"nixmox/internal/engine/task"
)

// BuildResult is local build evidence, not deployment or approval evidence. Root
// remains owned by the candidate/task until explicit retention cleanup.
type BuildResult struct {
	Output          domain.StoreOutput `json:"output"`
	Root            string             `json:"root"`
	InputLockDigest domain.Digest      `json:"input_lock_digest"`
}

// Build realizes one pinned system with a Nix-managed GC root from the outset.
// Callers persist the exact request under owner before invoking/reconciling this
// operation. Reusing owner for a different source or system is forbidden.
func (n Engine) Build(ctx context.Context, request engine.BuildRequest, owner domain.ID) (BuildResult, error) {
	var result BuildResult
	if n.Runner == nil || owner == "" || !filepath.IsAbs(n.PinDirectory) {
		return result, errors.New("build runner, owner and absolute root directory required")
	}
	if request.Source.Recipe != nil {
		return result, domain.Unavailable("recipe realization")
	}
	plan, err := n.BuildPlan(request)
	if err != nil {
		return result, err
	}
	result.InputLockDigest, err = lockDigest(request)
	if err != nil {
		return result, err
	}
	if err = os.MkdirAll(n.PinDirectory, 0700); err != nil {
		return result, err
	}
	result.Root = filepath.Join(n.PinDirectory, "candidate-"+shared.Filename(owner))
	// Nix itself registers this result link as an indirect GC root. Do not build
	// with --no-link and race collection while adding a root afterward.
	index := slices.Index(plan.Args, "--no-link")
	if index < 0 {
		return result, errors.New("build plan is missing output-link policy")
	}
	plan.Args = slices.Delete(plan.Args, index, index+1)
	plan.Args = append(plan.Args, "--out-link", result.Root)
	data, err := task.Capture(ctx, n.Runner, plan, 1<<20)
	if err != nil {
		return result, err
	}
	var builds []struct {
		Outputs map[string]string `json:"outputs"`
	}
	if json.Unmarshal(data, &builds) != nil || len(builds) != 1 || len(builds[0].Outputs) != 1 {
		return result, errors.New("build did not report exactly one system output")
	}
	output := builds[0].Outputs["out"]
	if !validStorePath(output) {
		return result, errors.New("build returned an invalid system output")
	}
	linked, err := filepath.EvalSymlinks(result.Root)
	if err != nil || linked != output {
		return result, errors.New("build root does not identify the reported output")
	}
	directory, err := os.Open(n.PinDirectory)
	if err != nil {
		return result, err
	}
	if err = errors.Join(directory.Sync(), directory.Close()); err != nil {
		return result, err
	}
	result.Output, err = n.ObserveOutput(ctx, output)
	return result, err
}

func lockDigest(request engine.BuildRequest) (domain.Digest, error) {
	root, err := os.OpenRoot(request.Worktree)
	if err != nil {
		return "", err
	}
	defer root.Close()
	file, err := root.Open(filepath.Join(request.Source.FlakeDirectory, "flake.lock"))
	if errors.Is(err, os.ErrNotExist) {
		sum := sha256.Sum256(nil)
		return domain.Digest(hex.EncodeToString(sum[:])), nil
	}
	if err != nil {
		return "", err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (8<<20)+1))
	if err != nil || len(data) > 8<<20 {
		return "", errors.New("input lock exceeds build metadata bounds")
	}
	sum := sha256.Sum256(data)
	return domain.Digest(hex.EncodeToString(sum[:])), nil
}
