package service

import (
	"encoding/json"
	"nixmox/internal/domain"
	"time"
)

func (e *Execution) SetSource(source domain.Source, builder string) error {
	s := e.service
	s.mu.Lock()
	defer s.mu.Unlock()
	task := s.tasks[e.TaskID]
	if task.Terminal() {
		return domain.Err("INVALID_STATE", "Terminal task is immutable.", 409)
	}
	task.Source = source
	task.BuilderID = &builder
	task.Version++
	task.UpdatedAt = time.Now().UTC()
	if err := s.persistTask(task); err != nil {
		return err
	}
	s.notify()
	return nil
}
func (e *Execution) SetProgress(progress any) error {
	b, err := json.Marshal(progress)
	if err != nil || len(b) > 32<<10 {
		return domain.Err("INVALID_PROGRESS", "Progress exceeds its bound.", 422)
	}
	s := e.service
	s.mu.Lock()
	defer s.mu.Unlock()
	task := s.tasks[e.TaskID]
	if task.Terminal() {
		return domain.Err("INVALID_STATE", "Terminal task is immutable.", 409)
	}
	task.Progress = json.RawMessage(b)
	task.Version++
	task.UpdatedAt = time.Now().UTC()
	if err = s.persistTask(task); err != nil {
		return err
	}
	s.notify()
	return nil
}
