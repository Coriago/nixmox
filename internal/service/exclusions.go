package service

import (
	"context"
	"strings"
	"time"

	"nixmox/internal/domain"
)

func validateExclusions(keys []string) error {
	seen := map[string]bool{}
	if len(keys) > 32 {
		return domain.Err("INVALID_STATE", "Too many resource exclusions.", 422)
	}
	for _, key := range keys {
		if key == "" || len(key) > 256 || strings.ContainsAny(key, "\x00\r\n") || seen[key] {
			return domain.Err("INVALID_STATE", "Invalid resource exclusion.", 422)
		}
		seen[key] = true
	}
	return nil
}

// Called under mu. Persisted exclusions survive blocked/reconciling states;
// expiry of a remote lease cannot authorize overlapping local effects.
func (s *Service) exclusionsAvailable(id string, keys []string) bool {
	for _, t := range s.tasks {
		if t.ID == id || t.Terminal() {
			continue
		}
		for _, held := range t.Exclusions {
			for _, key := range keys {
				if key == held {
					return false
				}
			}
		}
	}
	return true
}

// Exclude atomically replaces the phase's complete resource set. Callers release
// prior effects' resources only after those effects have stopped or reconciled.
// A conflicting request returns a blocked prerequisite instead of occupying all
// workers while waiting for another queued task.
func (e *Execution) Exclude(keys ...string) error {
	if err := validateExclusions(keys); err != nil {
		return err
	}
	s := e.service
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.tasks[e.TaskID]
	if t.Terminal() {
		return domain.Err("INVALID_STATE", "Terminal tasks are immutable.", 409)
	}
	if t.State == "cancelling" {
		return context.Canceled
	}
	if !s.exclusionsAvailable(t.ID, keys) {
		t.WaitingFor = append([]string(nil), keys...)
		if err := s.persistTask(t); err != nil {
			return unknown()
		}
		return Block(t.Phase, "RESOURCE_BUSY", "Another task holds a required resource.")
	}
	t.WaitingFor = nil
	t.Exclusions = append([]string(nil), keys...)
	t.Version++
	t.UpdatedAt = time.Now().UTC()
	if err := s.persistTask(t); err != nil {
		return unknown()
	}
	s.notify()
	s.signal()
	return nil
}

// Resume is called only by typed feature handlers after validating the recorded
// prerequisite (including approval evidence), never directly by a transport.
// It preserves checkpoints and requires reconciliation before effects resume.
func (s *Service) Resume(id string, expectedVersion int64, validate func(domain.Task) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[id]
	if !ok {
		return domain.Err("NOT_FOUND", "Task not found.", 404)
	}
	if t.Version != expectedVersion {
		return domain.Err("VERSION_CONFLICT", "Task changed after review.", 409)
	}
	if t.State != "blocked" || validate == nil {
		return domain.Err("INVALID_STATE", "Task cannot resume.", 409)
	}
	if err := validate(t); err != nil {
		return err
	}
	if err := t.Transition("reconciling", t.Phase); err != nil {
		return err
	}
	t.Error = nil
	if err := s.persistTask(t); err != nil {
		return unknown()
	}
	s.notify()
	s.signal()
	return nil
}
