// Package nixstore isolates Nix evaluation, realization and verified store metadata.
package nixstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"nixmox/internal/domain"
	"nixmox/internal/gitstore"
	"nixmox/internal/process"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var PathPattern = regexp.MustCompile(`^/nix/store/[0-9abcdfghijklmnpqrsvwxyz]{32}-[^/\x00\r\n]+$`)
var AttributePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*(\.[A-Za-z_][A-Za-z0-9_-]*)*$`)

type Nix struct{ Substituters []string }
type Evaluation struct {
	System          string `json:"system"`
	Derivation      string `json:"derivation"`
	InputLockDigest string `json:"input_lock_digest"`
}
type Output struct {
	Path       string `json:"path"`
	NarHash    string `json:"nar_hash"`
	NarSize    int64  `json:"nar_size"`
	Derivation string `json:"derivation"`
}

func arguments(verb string) []string {
	return []string{verb, "--json", "--no-write-lock-file", "--no-update-lock-file", "--option", "accept-flake-config", "false"}
}
func (n Nix) Evaluate(ctx context.Context, dir, attribute string) (Evaluation, error) {
	return n.evaluate(ctx, dir, attribute, "path:"+dir, true)
}
func (n Nix) EvaluateGit(ctx context.Context, root, subdir, commit, attribute string) (Evaluation, error) {
	if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(commit) || !filepath.IsAbs(root) || filepath.IsAbs(subdir) || filepath.Clean(subdir) != subdir || subdir == ".." || strings.HasPrefix(subdir, "../") {
		return Evaluation{}, domain.Err("INVALID_SOURCE", "Invalid pinned Git flake.", 422)
	}
	q := url.Values{"rev": {commit}}
	if subdir != "." {
		q.Set("dir", subdir)
	}
	u := url.URL{Scheme: "git+file", Path: root, RawQuery: q.Encode()}
	e, err := n.evaluate(ctx, filepath.Join(root, subdir), attribute, u.String(), false)
	if err != nil {
		return e, err
	}
	lock, err := gitstore.ReadWorktreeFile(ctx, root, commit, filepath.ToSlash(filepath.Join(subdir, "flake.lock")))
	if err != nil {
		var failure *domain.Error
		if !errors.As(err, &failure) || failure.Code != "NOT_FOUND" {
			return e, err
		}
	}
	sum := sha256.Sum256(lock)
	e.InputLockDigest = hex.EncodeToString(sum[:])
	return e, nil
}
func (n Nix) evaluate(ctx context.Context, dir, attribute, source string, readLock bool) (Evaluation, error) {
	var e Evaluation
	if !filepath.IsAbs(dir) || !AttributePattern.MatchString(attribute) {
		return e, domain.Err("INVALID_SOURCE", "Invalid flake source descriptor.", 422)
	}
	args := arguments("eval")
	args = append(args, "--option", "allow-import-from-derivation", "false", source+"#"+attribute, "--apply", `c: { system = c.pkgs.stdenv.hostPlatform.system; derivation = c.config.system.build.toplevel.drvPath; }`)
	r, err := process.Run(ctx, process.Options{Timeout: 2 * time.Minute, MaxOutput: 1 << 20}, "nix", args...)
	if err != nil || json.Unmarshal(r.Stdout, &e) != nil || !PathPattern.MatchString(e.Derivation) || !strings.HasSuffix(e.Derivation, ".drv") {
		return e, domain.Err("EVALUATION_FAILED", "Pinned configuration evaluation failed; lightweight evaluation disables import-from-derivation and lock updates.", 422)
	}
	if !readLock {
		return e, nil
	}
	lock, err := os.ReadFile(filepath.Join(dir, "flake.lock"))
	if err != nil && !os.IsNotExist(err) {
		return e, err
	}
	if len(lock) > 4<<20 {
		return e, domain.Err("STORAGE_LIMIT", "Input lock exceeds 4 MiB.", 507)
	}
	sum := sha256.Sum256(lock)
	e.InputLockDigest = hex.EncodeToString(sum[:])
	return e, nil
}
func (n Nix) Build(ctx context.Context, e Evaluation, root string, onEvent func([]byte)) (Output, error) {
	if !PathPattern.MatchString(e.Derivation) || !strings.HasSuffix(e.Derivation, ".drv") || !filepath.IsAbs(root) {
		return Output{}, domain.Err("INVALID_SOURCE", "Build requires a pinned derivation and private output root.", 422)
	}
	if err := os.MkdirAll(filepath.Dir(root), 0700); err != nil {
		return Output{}, err
	}
	// Realize the evaluated derivation, never re-resolve a branch or attribute.
	args := []string{"build", "--json", "--builders", "", "--out-link", root, "--log-format", "internal-json", "--option", "connect-timeout", "5", "--option", "download-attempts", "2", "--option", "stalled-download-timeout", "15", "--fallback"}
	if n.Substituters != nil {
		args = append(args, "--option", "substituters", strings.Join(n.Substituters, " "))
	}
	args = append(args, e.Derivation+"^out")
	r, err := process.Run(ctx, process.Options{Timeout: 2 * time.Hour, MaxOutput: 1 << 20, StderrLine: onEvent}, "nix", args...)
	if err != nil {
		if ctx.Err() != nil {
			return Output{}, ctx.Err()
		}
		return Output{}, domain.Err("BUILD_FAILED", "Pinned derivation realization failed; inspect structured build activities.", 500)
	}
	var builds []struct {
		DrvPath string            `json:"drvPath"`
		Outputs map[string]string `json:"outputs"`
	}
	if json.Unmarshal(r.Stdout, &builds) != nil || len(builds) != 1 || builds[0].DrvPath != e.Derivation {
		return Output{}, domain.Err("OUTPUT_INVALID", "Nix did not return the pinned derivation output.", 409)
	}
	output, err := n.Info(ctx, builds[0].Outputs["out"])
	if err != nil {
		return output, err
	}
	output.Derivation = e.Derivation
	if info, err := os.Stat(filepath.Join(output.Path, "bin/switch-to-configuration")); err != nil || info.Mode()&0111 == 0 {
		return Output{}, domain.Err("OUTPUT_INVALID", "Built output is not an activatable NixOS system.", 409)
	}
	return output, nil
}
func (n Nix) Info(ctx context.Context, path string) (Output, error) {
	if !PathPattern.MatchString(path) {
		return Output{}, domain.Err("OUTPUT_INVALID", "Invalid Nix store output.", 422)
	}
	r, err := process.Run(ctx, process.Options{Timeout: 10 * time.Second, MaxOutput: 1 << 20}, "nix", "path-info", "--json", path)
	var info map[string]struct {
		NarHash string `json:"narHash"`
		NarSize int64  `json:"narSize"`
		Deriver string `json:"deriver"`
	}
	if err != nil || json.Unmarshal(r.Stdout, &info) != nil {
		return Output{}, domain.Err("OUTPUT_UNAVAILABLE", "Verified local store metadata is unavailable.", 503)
	}
	value, ok := info[path]
	if !ok || !strings.HasPrefix(value.NarHash, "sha256-") || value.NarSize < 0 {
		return Output{}, domain.Err("OUTPUT_INVALID", "Store output metadata is invalid.", 409)
	}
	return Output{Path: path, NarHash: value.NarHash, NarSize: value.NarSize, Derivation: value.Deriver}, nil
}
