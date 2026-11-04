package deployment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"nixmox/internal/domain"
	"nixmox/internal/host"
	"nixmox/internal/installer"
	"nixmox/internal/installer/installers"
	"nixmox/internal/task"
)

const InstallationTask = "fresh-deployment"

type Installation struct {
	SystemPath  string                `json:"systemPath"`
	DiskoScript string                `json:"diskoScript"`
	MountScript string                `json:"mountScript"`
	Root        string                `json:"root"`
	Devices     []string              `json:"devices"`
	Destination installer.Destination `json:"destination"`
}

type InstallationRuntime struct {
	Inspect  func(context.Context) (installers.Target, uint64, error)
	Capture  func(context.Context, *task.Execution, string, installer.Destination) (installer.Preservation, error)
	Preserve func(context.Context, *task.Execution, string, string, installer.Destination) error
	Validate func(context.Context, *task.Execution, installer.Preservation) error
	Promote  func(context.Context, *task.Execution) (domain.Task, error)
}

func (s Service) InstallRunner(ctx context.Context, execution *task.Execution) (any, error) {
	if s.Installation == nil {
		return nil, errors.New("installation runtime unavailable")
	}
	var input Input
	if err := json.Unmarshal(execution.Task().Inputs, &input); err != nil {
		return nil, err
	}
	if err := s.assigned(ctx, input); err != nil {
		return nil, err
	}
	target, generation, err := s.Installation.Inspect(ctx)
	if err != nil {
		return nil, err
	}
	if target.Environment == "installed" {
		var expected installer.Preservation
		if err = json.Unmarshal(execution.Task().Context["installation"], &expected); err != nil {
			return nil, err
		}
		if target.NodeID != expected.NodeID || target.BootID == expected.PreviousBootID || target.SystemPath != expected.ExpectedSystem || generation != expected.ExpectedGeneration {
			return nil, errors.New("installed boot does not match installation contract")
		}
		if err = s.Installation.Validate(ctx, execution, expected); err != nil {
			return nil, err
		}
		if err = execution.Checkpoint(ctx, "validatedInstallation", true); err != nil {
			return nil, err
		}
		if err = execution.ReleaseResources(ctx); err != nil {
			return nil, err
		}
		child, err := s.Installation.Promote(ctx, execution)
		if err != nil {
			return nil, err
		}
		if err = execution.LinkChild(ctx, domain.ChildTask{TaskID: child.ID, Role: "promotion"}); err != nil {
			return nil, err
		}
		if _, err = execution.WaitChild(ctx, child.ID, "node-promotion", child.HostID); err != nil {
			return nil, err
		}
		return struct {
			Result
			MembershipStatus string `json:"membershipStatus"`
		}{Result{StorePath: target.SystemPath, Generation: generation}, "full"}, nil
	}
	if target.Environment != "installer" || target.Container {
		return nil, errors.New("fresh installation requires a non-container installer boot")
	}
	if len(execution.Task().Context["validatedInstallation"]) > 0 {
		return nil, errors.New("validated installation returned to an installer; refusing another disk operation")
	}
	built, err := execution.Child(ctx, "installation-build", "build", input)
	if err != nil {
		return nil, err
	}
	var plan Installation
	if err = json.Unmarshal(built, &plan); err != nil {
		return nil, err
	}
	if len(execution.Task().Context["installation"]) == 0 {
		expected, err := s.Installation.Capture(ctx, execution, plan.SystemPath, plan.Destination)
		if err != nil {
			return nil, err
		}
		if err = execution.Checkpoint(ctx, "installation", expected); err != nil {
			return nil, err
		}
	}
	if !input.AutoApprove {
		if err = execution.Review(ctx, "installation-plan", plan); err != nil {
			return nil, err
		}
	}
	if err = s.assigned(ctx, input); err != nil {
		return nil, err
	}
	log := execution.LogWriter(ctx)
	defer log.Close()
	process := host.Process{Log: log}
	// Root all three outputs before any device changes or reboot handoff.
	for index, path := range []string{plan.SystemPath, plan.DiskoScript, plan.MountScript} {
		root := fmt.Sprintf("/nix/var/nix/gcroots/nixmox-%s-%d", execution.Task().ID, index)
		if _, err = process.Run(ctx, "", nil, "nix-store", "--realise", path, "--add-root", root); err != nil {
			return nil, err
		}
	}
	if len(execution.Task().Context["disksPrepared"]) == 0 {
		if len(execution.Task().Context["diskPreparationStarted"]) > 0 {
			return nil, errors.New("interrupted disk preparation requires inspection; refusing automatic reformat")
		}
		if err = validateInstallationDisks(ctx, process, plan); err != nil {
			return nil, err
		}
		if err = execution.Checkpoint(ctx, "diskPreparationStarted", true); err != nil {
			return nil, err
		}
		if _, err = process.Run(ctx, "", nil, plan.DiskoScript, "--yes-wipe-all-disks"); err != nil {
			return nil, err
		}
		if err = execution.Checkpoint(ctx, "disksPrepared", true); err != nil {
			return nil, err
		}
	} else {
		if _, err = process.Run(ctx, "", nil, plan.MountScript); err != nil {
			return nil, err
		}
	}
	if _, err = execution.Child(ctx, installer.FinalizeTask, "finalize", installer.FinalizeInput{FinalizeID: "finalize-general", Root: plan.Root, SystemPath: plan.SystemPath, Destination: plan.Destination}); err != nil {
		return nil, err
	}
	if err = execution.Checkpoint(ctx, "installed", true); err != nil {
		return nil, err
	}
	if err = s.Installation.Preserve(ctx, execution, plan.Root, plan.SystemPath, plan.Destination); err != nil {
		return nil, err
	}
	syscall.Sync()
	if _, err = process.Run(ctx, "", nil, "systemctl", "--no-block", "reboot"); err != nil {
		return nil, err
	}
	return nil, task.ErrRestart
}

func validateInstallationDisks(ctx context.Context, process host.Process, plan Installation) error {
	if !filepath.IsAbs(plan.Root) || filepath.Clean(plan.Root) == "/" || len(plan.Devices) == 0 {
		return errors.New("separate installation root and explicit disk devices required")
	}
	if _, err := process.Run(ctx, "", nil, "mountpoint", "--quiet", plan.Root); err == nil {
		return errors.New("destination is already mounted before disk preparation")
	}
	inventory, err := process.Disks(ctx)
	if err != nil {
		return err
	}
	available := map[string]host.Disk{}
	for _, disk := range inventory {
		path, err := filepath.EvalSymlinks(disk.Device)
		if err != nil {
			return err
		}
		available[path] = disk
	}
	selected := map[string]bool{}
	for _, device := range plan.Devices {
		path, err := filepath.EvalSymlinks(device)
		if err != nil {
			return err
		}
		disk, ok := available[path]
		if !ok || selected[path] || disk.ReadOnly || disk.Mounted || disk.Size == 0 {
			return fmt.Errorf("disk %s is absent, duplicate, mounted, or not writable", device)
		}
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeDevice == 0 || info.Mode()&os.ModeCharDevice != 0 {
			return errors.New("installation requires whole block disks")
		}
		selected[path] = true
	}
	return nil
}
