package installer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"nixmox/internal/domain"
	"nixmox/internal/installer/finalize"
	"nixmox/internal/task"
)

const FinalizeTask = "install-finalize"

type FinalizeInput struct {
	FinalizeID string `json:"finalizeId,omitempty"`
	Root       string `json:"root"`
	SystemPath string `json:"systemPath"`
}

func (s *Service) Finalize(ctx context.Context, id string, input FinalizeInput) (domain.Task, error) {
	if input.FinalizeID == "" {
		input.FinalizeID = "finalize-general"
	}
	if input.FinalizeID != "finalize-general" {
		return domain.Task{}, fmt.Errorf("%w: unknown finalizer", ErrInvalid)
	}
	target, err := s.target(ctx, id)
	if err != nil {
		return domain.Task{}, err
	}
	check := (finalize.General{}).Check(finalize.Installation{Target: target, Root: input.Root, SystemPath: input.SystemPath})
	if !check.Compatible {
		return domain.Task{}, fmt.Errorf("%w: %s", ErrInvalid, check.Reason)
	}
	if err = s.ready(ctx, target); err != nil {
		return domain.Task{}, err
	}
	data, err := json.Marshal(input)
	if err != nil {
		return domain.Task{}, err
	}
	return s.Tasks.Submit(ctx, domain.Task{HostID: id, Type: FinalizeTask, Inputs: data,
		Concurrency: domain.TaskConcurrency{ExclusiveResources: []string{"host:" + id + ":system"}}})
}

func (s *Service) FinalizeRunner(ctx context.Context, execution *task.Execution) (any, error) {
	var input FinalizeInput
	if err := json.Unmarshal(execution.Task().Inputs, &input); err != nil {
		return nil, err
	}
	if input.FinalizeID != "finalize-general" {
		return nil, errors.New("unknown finalizer")
	}
	if saved, ok := execution.Task().Context["finalized"]; ok {
		var completed FinalizeInput
		if err := json.Unmarshal(saved, &completed); err != nil {
			return nil, err
		}
		if completed != input {
			return nil, errors.New("finalization checkpoint differs from task inputs")
		}
		return completed, nil
	}
	target, err := s.target(ctx, execution.Task().HostID)
	if err != nil {
		return nil, err
	}
	if err = s.ready(ctx, target); err != nil {
		return nil, err
	}
	log := execution.LogWriter(ctx)
	method := finalize.General{}
	method.Process.Log = log
	err = method.Execute(ctx, finalize.Installation{
		Target: target, Root: input.Root, SystemPath: input.SystemPath,
		Preserve: func(ctx context.Context, root string) error {
			return s.Transition.PreserveInstalled(ctx, execution, root)
		},
	})
	err = errors.Join(err, log.Close())
	if err != nil {
		return nil, err
	}
	if err = execution.Checkpoint(ctx, "finalized", input); err != nil {
		return nil, err
	}
	return input, nil
}
