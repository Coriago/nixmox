package deployment

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/nix-community/go-nix/pkg/storepath"

	"nixmox/internal/host"
	"nixmox/internal/task"
)

type Result struct {
	StorePath  string `json:"storePath"`
	Generation uint64 `json:"generation"`
}

func (s Service) Runner(ctx context.Context, execution *task.Execution) (any, error) {
	var input Input
	if err := json.Unmarshal(execution.Task().Inputs, &input); err != nil {
		return nil, err
	}
	if err := s.assigned(ctx, input); err != nil {
		return nil, err
	}
	built, err := execution.Child(ctx, "configuration-build", "build", input)
	if err != nil {
		return nil, err
	}
	var result host.BuildResult
	if err = json.Unmarshal(built, &result); err != nil {
		return nil, err
	}
	if len(result.StorePaths) != 1 || !storepath.PathRe.MatchString(result.StorePaths[0]) {
		return nil, errors.New("deployment requires one NixOS system output")
	}
	proposed := result.StorePaths[0]
	current, err := filepath.EvalSymlinks("/run/current-system")
	if err != nil {
		return nil, err
	}
	log := execution.LogWriter(ctx)
	defer log.Close()
	process := host.Process{Log: log}
	if !input.AutoApprove {
		diff, err := process.Run(ctx, "", nil, "nix", "store", "diff-closures", "--json", current, proposed)
		if err != nil {
			return nil, err
		}
		if err = execution.Review(ctx, "deployment-diff", map[string]any{"current": map[string]string{"storePath": current}, "proposed": map[string]string{"storePath": proposed}, "packages": json.RawMessage(diff)}); err != nil {
			return nil, err
		}
	}
	if err = s.assigned(ctx, input); err != nil {
		return nil, err
	}
	// Keep a GC root across the profile/activation transition and manual recovery.
	root := "/nix/var/nix/gcroots/nixmox-" + execution.Task().ID
	if _, err = process.Run(ctx, "", nil, "nix-store", "--realise", proposed, "--add-root", root); err != nil {
		return nil, err
	}
	defer os.Remove(root)
	if err = execution.Checkpoint(ctx, "activation", map[string]string{"previous": current, "proposed": proposed}); err != nil {
		return nil, err
	}
	if _, err = process.Run(ctx, "", nil, "nix-env", "--profile", "/nix/var/nix/profiles/system", "--set", proposed); err != nil {
		return nil, err
	}
	if _, err = process.Run(ctx, "", nil, proposed+"/bin/switch-to-configuration", "switch"); err != nil {
		return nil, err
	}
	running, err := filepath.EvalSymlinks("/run/current-system")
	if err != nil {
		return nil, err
	}
	if running != proposed {
		return nil, errors.New("activation did not select the proposed system")
	}
	link, err := os.Readlink("/nix/var/nix/profiles/system")
	if err != nil {
		return nil, err
	}
	number := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(link), "system-"), "-link")
	generation, err := strconv.ParseUint(number, 10, 64)
	if err != nil {
		return nil, err
	}
	return Result{StorePath: proposed, Generation: generation}, nil
}
