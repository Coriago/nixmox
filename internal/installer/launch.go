package installer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"nixmox/internal/domain"
	"nixmox/internal/installer/installers"
	"nixmox/internal/installer/prepare"
	"nixmox/internal/task"
)

const LaunchTask = "launch-installer"

type LaunchInput struct {
	InstallerID string `json:"installerId,omitempty"`
	PrepareID   string `json:"prepareId,omitempty"`
	BuildHostID string `json:"buildHostId,omitempty"`
}

// LaunchPlan captures the immutable build pin and choices before submission.
type LaunchPlan struct {
	Installer installers.Definition `json:"installer"`
	PrepareID string                `json:"prepareId"`
}

type LaunchResult struct {
	InstallerID string `json:"installerId"`
	Environment string `json:"environment"`
	BootID      string `json:"bootId"`
}

type launchTransition struct {
	PreviousBootID string `json:"previousBootId"`
	SystemPath     string `json:"systemPath"`
}

func (s *Service) Launch(ctx context.Context, id string, input LaunchInput) (domain.Task, error) {
	if input.BuildHostID != "" && input.BuildHostID != id {
		return domain.Task{}, fmt.Errorf("%w: remote build dispatch is not implemented", ErrUnavailable)
	}
	checks, err := s.Checks(ctx, id, input.InstallerID, input.PrepareID)
	if err != nil {
		return domain.Task{}, err
	}
	if checks.Selected == nil {
		return domain.Task{}, fmt.Errorf("%w: no compatible installer/prepare pair", ErrInvalid)
	}
	if !checks.ExecutionReady {
		return domain.Task{}, fmt.Errorf("%w: %s", ErrUnavailable, checks.ExecutionBlocker)
	}
	plan := LaunchPlan{Installer: checks.Selected.Installer, PrepareID: checks.Selected.Prepare.ID}
	data, err := json.Marshal(plan)
	if err != nil {
		return domain.Task{}, err
	}
	return s.Tasks.Submit(ctx, domain.Task{HostID: id, Type: LaunchTask, Inputs: data,
		Concurrency: domain.TaskConcurrency{ExclusiveResources: []string{"host:" + id + ":system"}}})
}

func (s *Service) Runner(ctx context.Context, execution *task.Execution) (any, error) {
	var plan LaunchPlan
	if err := json.Unmarshal(execution.Task().Inputs, &plan); err != nil {
		return nil, err
	}
	target, err := s.target(ctx, execution.Task().HostID)
	if err != nil {
		return nil, err
	}
	// On recovery, validate the returning boot before any build or destructive step.
	if saved, ok := execution.Task().Context["installerTransition"]; ok {
		var transition launchTransition
		if err = json.Unmarshal(saved, &transition); err != nil {
			return nil, err
		}
		if target.BootID == "" || transition.PreviousBootID == "" || target.BootID == transition.PreviousBootID || target.Environment != "installer" {
			return nil, errors.New("installer transition not verified; refusing to repeat kexec")
		}
		if target.SystemPath == "" || target.SystemPath != transition.SystemPath {
			return nil, errors.New("returning installer differs from staged system")
		}
		if target.System != plan.Installer.System {
			return nil, errors.New("returning installer architecture differs from launch plan")
		}
		return LaunchResult{InstallerID: plan.Installer.ID, Environment: target.Environment, BootID: target.BootID}, nil
	}
	if err = s.ready(ctx, target); err != nil {
		return nil, err
	}
	_, selected := Select(target, []installers.Definition{plan.Installer}, plan.Installer.ID, plan.PrepareID)
	if selected == nil {
		return nil, errors.New("host no longer compatible with launch plan")
	}
	built, err := s.build(ctx, execution, plan.Installer.Build)
	if err != nil {
		return nil, err
	}
	// Re-probe after a potentially long build, before membership or boot changes.
	target, err = s.target(ctx, execution.Task().HostID)
	if err != nil {
		return nil, err
	}
	_, selected = Select(target, []installers.Definition{plan.Installer}, plan.Installer.ID, plan.PrepareID)
	if selected == nil {
		return nil, errors.New("host compatibility changed during build")
	}
	if err = s.ready(ctx, target); err != nil {
		return nil, err
	}
	if target.BootID == "" {
		return nil, errors.New("current boot ID required")
	}
	if err = s.Transition.Demote(ctx, target); err != nil {
		return nil, err
	}
	boot, err := s.Transition.Stage(ctx, execution, built)
	if err != nil {
		return nil, err
	}
	if err = boot.Validate(); err != nil {
		return nil, err
	}
	if err = execution.Checkpoint(ctx, "installerTransition", launchTransition{PreviousBootID: target.BootID, SystemPath: boot.SystemPath}); err != nil {
		return nil, err
	}
	if err = s.Transition.Persist(ctx, execution); err != nil {
		return nil, err
	}
	log := execution.LogWriter(ctx)
	method := prepare.Kexec{}
	method.Process.Log = log
	err = method.Execute(ctx, boot)
	err = errors.Join(err, log.Close())
	if err != nil {
		return nil, err
	}
	// Dispatch is not completion. The returning daemon verifies the saved boot ID.
	<-ctx.Done()
	return nil, ctx.Err()
}
