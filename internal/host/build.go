package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nix-community/go-nix/pkg/narinfo/signature"
	"github.com/nix-community/go-nix/pkg/storepath"

	"nixmox/internal/source"
	"nixmox/internal/task"
)

type BuildInput struct {
	TargetRepo  source.Pin `json:"targetRepo"`
	FlakeDir    string     `json:"flakeDir"`
	Installable string     `json:"installable"`
}

type BuildResult struct {
	StorePaths []string `json:"storePaths"`
}

type Builder struct {
	Sources        source.Manager
	Repository     func(context.Context, string) (source.Repository, error)
	Process        Process
	CacheURL       string
	CachePublicKey string
}

func (b Builder) Run(ctx context.Context, input BuildInput) (BuildResult, error) {
	var result BuildResult
	if err := input.TargetRepo.Validate(); err != nil {
		return result, err
	}
	if input.FlakeDir == "" {
		input.FlakeDir = "."
	}
	if !filepath.IsLocal(input.FlakeDir) || strings.TrimSpace(input.Installable) == "" || strings.ContainsAny(input.Installable, "\x00\r\n#") {
		return result, errors.New("relative flake directory and installable attribute required")
	}
	if b.Repository == nil {
		return result, errors.New("repository resolver unavailable")
	}
	repo, err := b.Repository(ctx, input.TargetRepo.ID)
	if err != nil {
		return result, err
	}
	work, err := b.Sources.Checkout(ctx, repo, input.TargetRepo)
	if err != nil {
		return result, err
	}
	defer work.Close()
	resolved, err := work.Directory(input.FlakeDir)
	if err != nil {
		return result, err
	}
	args := []string{"build", "--json", "--no-link", "--print-build-logs", "--log-format", "internal-json"}
	if b.CacheURL != "" {
		if _, err := signature.ParsePublicKey(b.CachePublicKey); err != nil {
			return result, fmt.Errorf("invalid cache signing public key: %w", err)
		}
		args = append(args, "--extra-substituters", b.CacheURL, "--extra-trusted-public-keys", b.CachePublicKey)
	}
	args = append(args, "path:"+resolved+"#"+input.Installable)
	data, err := b.Process.Run(ctx, work.Path, nil, "nix", args...)
	if err != nil {
		return result, fmt.Errorf("Nix build failed: %w", err)
	}
	return ParseBuildResult(data)
}

// ParseBuildResult decodes nix build --json, using go-nix's canonical store-path
// grammar rather than accepting arbitrary filesystem paths under /nix/store.
func ParseBuildResult(data []byte) (BuildResult, error) {
	var result BuildResult
	var built []struct {
		Outputs map[string]string `json:"outputs"`
	}
	if err := json.Unmarshal(data, &built); err != nil {
		return result, err
	}
	seen := map[string]bool{}
	for _, item := range built {
		for _, path := range item.Outputs {
			if !storepath.PathRe.MatchString(path) {
				return result, errors.New("Nix returned invalid store path")
			}
			if !seen[path] {
				result.StorePaths = append(result.StorePaths, path)
				seen[path] = true
			}
		}
	}
	if len(result.StorePaths) == 0 {
		return result, errors.New("Nix build returned no outputs")
	}
	sort.Strings(result.StorePaths)
	return result, nil
}

func (b Builder) Runner(ctx context.Context, execution *task.Execution) (any, error) {
	var input BuildInput
	if err := json.Unmarshal(execution.Task().Inputs, &input); err != nil {
		return nil, err
	}
	log := execution.LogWriter(ctx)
	b.Process.Log = log
	result, err := b.Run(ctx, input)
	return result, errors.Join(err, log.Close())
}
