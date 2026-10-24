package task

import (
	"context"
	"testing"

	"nixmox/internal/domain"
	"nixmox/internal/engine"
)

func TestCancelledInvocationCannotEnterNextPhase(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(ctx, "node")
	defer manager.Close()
	started := make(chan struct{})
	definition := engine.TaskDefinition{Phase: "read", Interruptible: true, Run: func(ctx context.Context, e engine.Execution) error {
		close(started)
		<-ctx.Done()
		if err := e.Phase(ctx, "effect", false); err != nil {
			return err
		}
		t.Error("cancelled invocation entered effect phase")
		return nil
	}}
	receipt, err := manager.Accept(ctx, localEnvelope("phases"), definition, acknowledged)
	if err != nil {
		t.Fatal(err)
	}
	<-started
	current, _ := manager.Task(ctx, *receipt.TaskID)
	if _, err := manager.Accept(ctx, controlEnvelope("cancel-phases", "task.cancel", current), engine.TaskDefinition{}, acknowledged); err != nil {
		t.Fatal(err)
	}
	current = awaitTask(t, manager, current.ID)
	if current.State != domain.Cancelled || current.Phase != "read" {
		t.Fatal(current)
	}
}
