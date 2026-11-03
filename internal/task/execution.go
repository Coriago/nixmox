package task

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	"nixmox/internal/cluster"
	"nixmox/internal/domain"
)

type Execution struct {
	manager *Manager
	record  cluster.Record[domain.Task]
}

func (e *Execution) Task() domain.Task { return e.record.Value }

func (e *Execution) Checkpoint(ctx context.Context, key string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	task := e.record.Value
	task.Context[key] = data
	return e.save(ctx, task)
}

func (e *Execution) save(ctx context.Context, task domain.Task) error {
	revision, err := e.manager.Tasks.Update(ctx, e.record.Key, task, e.record.Revision)
	if err == nil {
		e.record.Value = task
		e.record.Revision = revision
	}
	return err
}

// Review commits an immutable structured output before entering the review
// state. On resumed execution, runners reuse checkpointed build results.
func (e *Execution) Review(ctx context.Context, kind string, content any) error {
	if e.record.Value.Review != nil {
		if e.record.Value.Review.ApprovedAt != nil {
			return nil
		}
		return ErrReview
	}
	data, err := json.Marshal(content)
	if err != nil {
		return err
	}
	output := domain.TaskOutput{ID: uuid.NewString(), TaskID: e.record.Key, Kind: kind, Content: data}
	if _, err = e.manager.Outputs.Create(ctx, output.ID, output); err != nil {
		return err
	}
	task := e.record.Value
	// The manager publishes manual-review only after the runner has returned.
	// Approval and cancellation therefore cannot race a still-active runner.
	task.Review = &domain.Review{OutputID: output.ID}
	task.Links.Outputs = append(task.Links.Outputs, domain.OutputRef{ID: output.ID, Kind: kind})
	if err = e.save(ctx, task); err != nil {
		return fmt.Errorf("persist review: %w", err)
	}
	return ErrReview
}
