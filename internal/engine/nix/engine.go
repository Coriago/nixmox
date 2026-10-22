// Package nix implements pinned Nix plans, structured progress and store operations.
package nix

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/nix-community/go-nix/pkg/nar"
	"github.com/nix-community/go-nix/pkg/storepath"

	"nixmox/internal/domain"
	"nixmox/internal/engine"
	"nixmox/internal/engine/internal/shared"
	"nixmox/internal/engine/task"
)

func validStorePath(path string) bool {
	p, err := storepath.FromAbsolutePath(path)
	return err == nil && p != nil && filepath.Clean(path) == path && !strings.HasSuffix(path, ".drv")
}

var (
	_ engine.Nix           = Engine{}
	_ engine.StoreCache    = NARExporter{}
	_ engine.PackageDiffer = ClosureDiffer{}
)

type Engine struct {
	Runner       engine.Runner
	PinDirectory string
}

func flakeRef(source domain.Source, worktree string, pinned bool) (string, error) {
	if !filepath.IsAbs(worktree) || source.FlakeAttribute == "" || strings.ContainsAny(source.FlakeAttribute, "\x00\n") {
		return "", errors.New("absolute source worktree and explicit attribute required")
	}
	dir := source.FlakeDirectory
	if dir == "" {
		dir = "."
	}
	if filepath.IsAbs(dir) || filepath.Clean(dir) != dir || dir == ".." || strings.HasPrefix(dir, "../") {
		return "", errors.New("unsafe flake directory")
	}
	if pinned {
		if !shared.ValidCommit(source.Commit) {
			return "", errors.New("build requires immutable source commit")
		}
		u := url.URL{Scheme: "git+file", Path: worktree}
		q := url.Values{"rev": []string{string(source.Commit)}, "dir": []string{dir}}
		u.RawQuery = q.Encode()
		return u.String(), nil
	}
	root, err := filepath.EvalSymlinks(worktree)
	if err != nil {
		return "", err
	}
	directory, err := filepath.EvalSymlinks(filepath.Join(root, dir))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, directory)
	if err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", errors.New("flake directory escapes worktree")
	}
	return "path:" + directory, nil
}

func (n Engine) BuildPlan(request engine.BuildRequest) (engine.CommandLine, error) {
	ref, err := flakeRef(request.Source, request.Worktree, true)
	if err != nil {
		return engine.CommandLine{}, err
	}
	attribute := request.Attribute
	if attribute == "" {
		attribute = request.Source.FlakeAttribute + ".config.system.build.toplevel"
	}
	if strings.ContainsAny(attribute, "\x00\n") {
		return engine.CommandLine{}, errors.New("invalid flake attribute")
	}
	if request.System != "x86_64-linux" && request.System != "aarch64-linux" {
		return engine.CommandLine{}, domain.Unavailable("requested build system")
	}
	return engine.CommandLine{Program: "nix", Args: []string{"build", "--json", "--no-link", "--verbose", "--log-format", "internal-json", "--no-write-lock-file", "--no-update-lock-file", "--system", request.System, ref + "#" + attribute}}, nil
}

func (n Engine) Validate(ctx context.Context, source domain.Source, worktree string) (domain.Validation, error) {
	return n.validate(ctx, source, worktree, true)
}

func (n Engine) ValidateDraft(ctx context.Context, source domain.Source, worktree string) (domain.Validation, error) {
	return n.validate(ctx, source, worktree, false)
}

func (n Engine) validate(ctx context.Context, source domain.Source, worktree string, pinned bool) (domain.Validation, error) {
	ref, err := flakeRef(source, worktree, pinned)
	if err != nil {
		return domain.Validation{}, err
	}
	_, err = task.Capture(ctx, n.Runner, engine.CommandLine{Program: "nix", Args: []string{"eval", "--json", "--no-write-lock-file", "--no-update-lock-file", "--option", "allow-import-from-derivation", "false", ref + "#" + source.FlakeAttribute + ".config.system.build.toplevel.drvPath"}}, domain.QueryTotalLimit)
	result := domain.Validation{State: "ready", Commit: &source.Commit, Missing: []string{}, Errors: []domain.FieldError{}}
	if err != nil {
		result.State = "invalid"
		result.Errors = append(result.Errors, domain.FieldError{Code: "EVALUATION_FAILED", Message: "Lightweight evaluation failed (IFD is disabled)"})
	}
	return result, err
}

func (n Engine) ActivationPlan(output string, mode domain.ActivationMode) (engine.CommandLine, error) {
	if !validStorePath(output) || (mode != domain.Switch && mode != domain.Boot) {
		return engine.CommandLine{}, errors.New("activation requires a verified output and switch/boot mode")
	}
	// System-profile registration is a separately checkpointed operation. This plan
	// activates the exact output, never resolves a flake or invokes an installer.
	return engine.CommandLine{Program: filepath.Join(output, "bin/switch-to-configuration"), Args: []string{string(mode)}}, nil
}

type pathInfo struct {
	Path       string        `json:"path"`
	NARHash    domain.Digest `json:"narHash"`
	NARSize    *uint64       `json:"narSize"`
	References []string      `json:"references"`
	Signatures []string      `json:"signatures"`
	Deriver    string        `json:"deriver"`
}

func decodePathInfo(data []byte) ([]pathInfo, error) {
	var list []pathInfo
	if len(data) > 0 && data[0] == '[' {
		err := json.Unmarshal(data, &list)
		return list, err
	}
	var mapping map[string]pathInfo
	if err := json.Unmarshal(data, &mapping); err != nil {
		return nil, err
	}
	for path, info := range mapping {
		info.Path = path
		list = append(list, info)
	}
	return list, nil
}

func (n Engine) pathInfo(ctx context.Context, path string, recursive bool) ([]pathInfo, error) {
	if !validStorePath(path) {
		return nil, errors.New("invalid Nix output path")
	}
	args := []string{"path-info", "--json"}
	if recursive {
		args = append(args, "--recursive")
	}
	args = append(args, path)
	data, err := task.Capture(ctx, n.Runner, engine.CommandLine{Program: "nix", Args: args}, domain.QueryTotalLimit)
	if err != nil {
		return nil, err
	}
	return decodePathInfo(data)
}

func (n Engine) ObserveOutput(ctx context.Context, path string) (domain.StoreOutput, error) {
	info, err := n.pathInfo(ctx, path, false)
	if err != nil {
		return domain.StoreOutput{}, err
	}
	if len(info) != 1 || info[0].Path != path || info[0].NARHash == "" || info[0].NARSize == nil {
		return domain.StoreOutput{}, errors.New("output metadata missing or mismatched")
	}
	p := info[0]
	var deriver *string
	if p.Deriver != "" && p.Deriver != "unknown-deriver" {
		deriver = &p.Deriver
	}
	return domain.StoreOutput{StorePath: p.Path, NARHash: p.NARHash, NARSize: *p.NARSize, References: p.References, Signatures: p.Signatures, Deriver: deriver}, nil
}

func (n Engine) Closure(ctx context.Context, path string) ([]domain.ClosurePath, error) {
	info, err := n.pathInfo(ctx, path, true)
	if err != nil {
		return nil, err
	}
	result := make([]domain.ClosurePath, 0, len(info))
	for _, p := range info {
		result = append(result, domain.ClosurePath{StorePath: p.Path, NARHash: p.NARHash, NARSize: p.NARSize, References: p.References})
	}
	// Store-name suffixes are not reliable package version metadata. Enrich from
	// derivation env when locally available, never invent pairings from ANSI/text.
	derivations := []string{}
	seen := map[string]bool{}
	for _, p := range info {
		if p.Deriver != "" && p.Deriver != "unknown-deriver" && !seen[p.Deriver] && len(derivations) < 128 {
			derivations = append(derivations, p.Deriver)
			seen[p.Deriver] = true
		}
	}
	if len(derivations) > 0 {
		data, e := task.Capture(ctx, n.Runner, engine.CommandLine{Program: "nix", Args: append([]string{"derivation", "show"}, derivations...)}, domain.QueryTotalLimit)
		if e == nil {
			var derivs map[string]struct {
				Env map[string]string `json:"env"`
			}
			if json.Unmarshal(data, &derivs) == nil {
				for i, p := range info {
					env := derivs[p.Deriver].Env
					if name := env["pname"]; name != "" {
						result[i].Name = &name
					}
					if version := env["version"]; version != "" {
						result[i].Version = &version
					}
				}
			}
		}
	}
	return result, nil
}

type storePin struct{ path, directory string }

func (p storePin) Path() string { return p.path }

func (p storePin) Close() error { return os.RemoveAll(p.directory) }

func (n Engine) Pin(ctx context.Context, path string, owner domain.ID) (engine.OutputPin, error) {
	if !validStorePath(path) || !filepath.IsAbs(n.PinDirectory) {
		return nil, errors.New("valid output and private pin directory required")
	}
	if err := os.MkdirAll(n.PinDirectory, 0700); err != nil {
		return nil, err
	}
	directory, err := os.MkdirTemp(n.PinDirectory, "pin-"+shared.Filename(owner)+"-")
	if err != nil {
		return nil, err
	}
	// With a non-derivation path and substitution disabled, --realise can only
	// root an already valid output. It cannot build or fetch during a cache read.
	_, err = n.Runner.Run(ctx, engine.CommandLine{Program: "nix-store", Args: []string{"--add-root", filepath.Join(directory, "root"), "--indirect", "--realise", "--option", "substitute", "false", path}}, io.Discard, io.Discard)
	if err != nil {
		os.RemoveAll(directory)
		return nil, err
	}
	return storePin{path, directory}, nil
}

func (n Engine) ObserveSystem(ctx context.Context) (domain.Generation, error) {
	running, err := filepath.EvalSymlinks("/run/current-system")
	if err != nil {
		return domain.Generation{}, err
	}
	output, err := n.ObserveOutput(ctx, running)
	if err != nil {
		return domain.Generation{}, err
	}
	// Running/profile/boot generation number evidence is collected by deployment
	// handlers, not guessed from a store basename.
	return domain.Generation{OutputPath: output.StorePath, NARHash: output.NARHash, Available: true, Running: true}, nil
}

type NARExporter struct{ Nix engine.Nix }

func (e NARExporter) Lookup(ctx context.Context, path string) (domain.StoreOutput, error) {
	return e.Nix.ObserveOutput(ctx, path)
}

func (e NARExporter) Export(ctx context.Context, output domain.StoreOutput, w io.Writer) error {
	pin, err := e.Nix.Pin(ctx, output.StorePath, "cache-export")
	if err != nil {
		return err
	}
	defer pin.Close()
	actual, err := e.Nix.ObserveOutput(ctx, output.StorePath)
	if err != nil {
		return err
	}
	if actual.NARHash != output.NARHash || actual.NARSize != output.NARSize {
		return errors.New("output changed before export")
	}
	return nar.DumpPath(contextWriter{ctx, w}, output.StorePath)
}

type contextWriter struct {
	ctx    context.Context
	writer io.Writer
}

func (w contextWriter) Write(p []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	return w.writer.Write(p)
}
