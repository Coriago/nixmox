package task

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"nixmox/internal/cluster"
	"nixmox/internal/domain"
)

// Child reuses a linked operation across manual-review reentry.
func (e *Execution) Child(ctx context.Context, kind, role string, input any) (json.RawMessage, error) {
	id := ""
	for _, child := range e.Task().Links.ChildTasks {
		if child.Role == role {
			id = child.TaskID
			break
		}
	}
	if id == "" {
		data, err := json.Marshal(input)
		if err != nil {
			return nil, err
		}
		child, err := e.manager.SubmitChild(ctx, domain.Task{HostID: e.Task().HostID, Persist: e.Task().Persist, Type: kind, Inputs: data, Links: domain.TaskLinks{ParentTaskID: e.Task().ID}}, role)
		if err != nil {
			return nil, err
		}
		id = child.ID
		if err = e.LinkChild(ctx, domain.ChildTask{TaskID: id, Role: role}); err != nil {
			return nil, err
		}
	}
	return e.WaitChild(ctx, id, kind, e.Task().HostID)
}

// WaitChild validates parentage and ownership for local or remote linked tasks.
func (e *Execution) WaitChild(ctx context.Context, id, kind, hostID string) (json.RawMessage, error) {
	done := errors.New("child completed")
	var result json.RawMessage
	err := e.manager.Tasks.Watch(ctx, id, func(record *cluster.Record[domain.Task], deleted bool) error {
		if deleted {
			return errors.New("child task deleted")
		}
		if record == nil {
			return nil
		}
		child := record.Value
		if child.HostID != hostID || child.Type != kind || child.Links.ParentTaskID != e.Task().ID {
			return errors.New("child task ownership mismatch")
		}
		if !child.Status.Terminal() {
			return nil
		}
		if child.Status != domain.Succeeded {
			return fmt.Errorf("child %s ended %s: %v", id, child.Status, child.Error)
		}
		result = child.Result
		return done
	})
	if errors.Is(err, done) {
		err = nil
	}
	return result, err
}
