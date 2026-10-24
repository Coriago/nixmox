package task

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"os"

	"nixmox/internal/domain"
	"nixmox/internal/engine/internal/shared"
	"nixmox/internal/logging"
)

// Logs are bounded session tails, not execution journals. Offsets survive trimming,
// so clients can distinguish missing output from an empty successful read.
type logBuffer struct {
	data   []byte
	offset int64
	closed bool
}

func logID(id *domain.ID) domain.ID {
	if id == nil {
		return ""
	}
	return *id
}

func (m *Manager) Append(ctx context.Context, task domain.ID, activity *domain.ID, data []byte) (domain.LogEvent, error) {
	event := domain.LogEvent{TaskID: task, ActivityID: activity, OwnerNodeID: m.node}
	if err := ctx.Err(); err != nil {
		return event, err
	}
	if len(data) > domain.LogEventLimit/2 {
		return event, errors.New("log chunk exceeds bound")
	}
	e, err := m.find(task)
	if err != nil {
		return event, err
	}
	e.mu.Lock()
	if terminal(e.current.State) {
		e.mu.Unlock()
		return event, errors.New("log is closed")
	}
	key := logID(activity)
	log := e.logs[key]
	if log == nil {
		if len(e.logs) >= 65 {
			e.mu.Unlock()
			return event, errors.New("activity log retention bound reached")
		}
		log = &logBuffer{}
		e.logs[key] = log
	}
	if log.closed {
		e.mu.Unlock()
		return event, errors.New("log is closed")
	}
	event.Offset = log.offset + int64(len(log.data))
	log.data = append(log.data, data...)
	bound := 256 << 10
	if activity != nil {
		bound = 16 << 10
	}
	if len(log.data) > bound {
		removed := len(log.data) - bound
		log.offset += int64(removed)
		log.data = bytes.Clone(log.data[removed:])
	}
	if activity == nil {
		e.current.Log.NextOffset = event.Offset + int64(len(data))
	}
	e.mu.Unlock()
	event.DataBase64 = base64.StdEncoding.EncodeToString(data)
	if len(data) > 0 {
		logging.From(ctx).Info(string(data), "event", "task.output", "task_id", task, "offset", event.Offset)
		if m.LogPublished != nil {
			m.LogPublished(ctx, event)
		}
	}
	return event, nil
}
func (m *Manager) Read(_ context.Context, task domain.ID, activity *domain.ID, offset int64, limit int) (domain.LogChunk, error) {
	chunk := domain.LogChunk{}
	if offset < 0 || limit < 1 || limit > domain.LogReadLimit {
		return chunk, errors.New("invalid log bounds")
	}
	e, err := m.find(task)
	if err != nil {
		return chunk, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	log := e.logs[logID(activity)]
	if log == nil {
		log = &logBuffer{}
	}
	end := log.offset + int64(len(log.data))
	if offset > end {
		return chunk, &domain.Error{Code: "RANGE_NOT_SATISFIABLE", Message: "Offset is after log end"}
	}
	chunk.Truncated, chunk.RetainedFrom = offset < log.offset, log.offset
	chunk.Offset = max(offset, log.offset)
	chunk.NextOffset = min(end, chunk.Offset+int64(limit))
	chunk.DataBase64 = base64.StdEncoding.EncodeToString(log.data[chunk.Offset-log.offset : chunk.NextOffset-log.offset])
	chunk.Complete = log.closed || terminal(e.current.State)
	return chunk, nil
}
func (m *Manager) CloseLog(_ context.Context, task domain.ID, activity *domain.ID) error {
	e, err := m.find(task)
	if err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if log := e.logs[logID(activity)]; log != nil {
		log.closed = true
	}
	return nil
}
func (m *Manager) Snapshot(ctx context.Context, task domain.ID, activity *domain.ID) (domain.Download, io.ReadCloser, error) {
	e, err := m.find(task)
	if err != nil {
		return domain.Download{}, nil, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	log := e.logs[logID(activity)]
	if log == nil {
		return domain.Download{}, nil, os.ErrNotExist
	}
	data := bytes.Clone(log.data)
	return domain.Download{OwnerNodeID: m.node, Filename: string(task) + ".log", ContentType: "text/plain", Bytes: int64(len(data))}, io.NopCloser(bytes.NewReader(data)), ctx.Err()
}
func (m *Manager) Progress(_ context.Context, id domain.ID) (domain.TaskProgress, error) {
	e, err := m.find(id)
	if err != nil {
		return domain.TaskProgress{}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.progress.TaskID == "" {
		return domain.TaskProgress{SchemaVersion: 1, TaskID: id, Observed: domain.Observed{Freshness: domain.Unknown}}, nil
	}
	return shared.Clone(e.progress), nil
}
func (m *Manager) Activity(_ context.Context, task, id domain.ID) (domain.Activity, error) {
	e, err := m.find(task)
	if err != nil {
		return domain.Activity{}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	activity, ok := e.activities[id]
	if !ok {
		return activity, os.ErrNotExist
	}
	return shared.Clone(activity), nil
}
