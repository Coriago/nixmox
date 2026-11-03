package installer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"nixmox/internal/cluster"
	"nixmox/internal/domain"
	"nixmox/internal/host"
	"nixmox/internal/task"
)

func (s *Service) build(ctx context.Context, execution *task.Execution, input host.BuildInput) (host.BuildResult, error) {
	var result host.BuildResult
	childID := ""
	for _, child := range execution.Task().Links.ChildTasks {
		if child.Role == "build" {
			childID = child.TaskID
			break
		}
	}
	if childID == "" {
		data, err := json.Marshal(input)
		if err != nil {
			return result, err
		}
		child, err := s.Tasks.Submit(ctx, domain.Task{HostID: s.HostID, Type: "build", Inputs: data,
			Links: domain.TaskLinks{ParentTaskID: execution.Task().ID}})
		if err != nil {
			return result, err
		}
		childID = child.ID
		if err = execution.LinkChild(ctx, domain.ChildTask{TaskID: childID, Role: "build"}); err != nil {
			return result, err
		}
	}
	done := errors.New("build completed")
	err := s.Tasks.Tasks.Watch(ctx, childID, func(record *cluster.Record[domain.Task], _ bool) error {
		if record == nil {
			return nil
		}
		child := record.Value
		if child.HostID != s.HostID || child.Type != "build" || child.Links.ParentTaskID != execution.Task().ID {
			return errors.New("installer build task ownership mismatch")
		}
		if !child.Status.Terminal() {
			return nil
		}
		if child.Status != domain.Succeeded {
			return fmt.Errorf("installer build %s ended %s", childID, child.Status)
		}
		if err := json.Unmarshal(child.Result, &result); err != nil {
			return err
		}
		if len(result.StorePaths) != 1 {
			return errors.New("installer build must return one bundle")
		}
		return done
	})
	if errors.Is(err, done) {
		return result, nil
	}
	return result, err
}
