package service

import (
	"context"
	"encoding/base64"

	"nixmox/internal/domain"
	"nixmox/internal/storage"
)

type RemoteLogReader interface {
	RemoteLogs(context.Context, string, int, int) (LogChunk, error)
	RemoteLogSnapshot(context.Context, string) (storage.Log, bool, error)
}

type LogChunk struct {
	Offset       int    `json:"offset"`
	Data         string `json:"data_base64"`
	NextOffset   int    `json:"next_offset"`
	RetainedFrom int    `json:"retained_from"`
	Complete     bool   `json:"complete"`
	Truncated    bool   `json:"truncated"`
}

func (c LogChunk) Bytes() ([]byte, error) { return base64.StdEncoding.DecodeString(c.Data) }

// Log accepts sanitized executor diagnostics. Credential adapters must redact
// secrets before calling it; command payloads and raw process environments are
// never automatically logged.
func (e *Execution) Log(data []byte) error {
	s := e.service
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.tasks[e.TaskID]
	if t.Terminal() {
		return domain.Err("INVALID_STATE", "Task log is closed.", 409)
	}
	if len(data) > storage.MaxLogAppend {
		return domain.Err("INVALID_STATE", "Log chunk exceeds 64 KiB.", 422)
	}
	end, err := s.store.AppendLog(t.ID, data)
	if err != nil {
		s.fatal = true
		return unknown()
	}
	t.Log.NextOffset = end
	// Log bytes have their own offsets; output must not invalidate approval versions.
	if err := s.persistTask(t); err != nil {
		return unknown()
	}
	s.notify()
	return nil
}

func (s *Service) Logs(id string, offset, limit int) (LogChunk, error) {
	return s.LogsContext(context.Background(), id, offset, limit)
}
func (s *Service) LogsContext(ctx context.Context, id string, offset, limit int) (LogChunk, error) {
	if offset < 0 || limit < 1 || limit > 1<<20 {
		return LogChunk{}, domain.Err("INVALID_QUERY", "Invalid log offset or limit.", 400)
	}
	s.mu.Lock()
	_, local := s.tasks[id]
	transport := s.clusterTransport
	s.mu.Unlock()
	if !local {
		if remote, ok := transport.(RemoteLogReader); ok {
			return remote.RemoteLogs(ctx, id, offset, limit)
		}
	}
	l, complete, err := s.LogSnapshot(id)
	if err != nil {
		return LogChunk{}, err
	}
	if offset < l.Start {
		err := domain.Err("LOG_TRUNCATED", "Requested bytes are no longer retained.", 410)
		err.Details = map[string]int{"retained_from": l.Start}
		return LogChunk{}, err
	}
	if offset > l.End() {
		return LogChunk{}, domain.Err("RANGE_NOT_SATISFIABLE", "Offset exceeds the log end.", 416)
	}
	length := min(limit, l.End()-offset)
	data := l.Data[offset-l.Start : offset-l.Start+length]
	return LogChunk{Offset: offset, Data: base64.StdEncoding.EncodeToString(data), NextOffset: offset + length, RetainedFrom: l.Start, Complete: complete, Truncated: l.Start > 0}, nil
}

func (s *Service) LogSnapshot(id string) (storage.Log, bool, error) {
	return s.LogSnapshotContext(context.Background(), id)
}
func (s *Service) LogSnapshotContext(ctx context.Context, id string) (storage.Log, bool, error) {
	s.mu.Lock()
	t, ok := s.tasks[id]
	if !ok {
		transport := s.clusterTransport
		s.mu.Unlock()
		if remote, ok := transport.(RemoteLogReader); ok {
			return remote.RemoteLogSnapshot(ctx, id)
		}
		return storage.Log{}, false, domain.Err("NOT_FOUND", "Task not found.", 404)
	}
	defer s.mu.Unlock()
	if s.fatal {
		return storage.Log{}, false, unknown()
	}
	l, err := s.store.ReadLog(id)
	if err != nil {
		return storage.Log{}, false, domain.Err("UNAVAILABLE", "Retained log is unavailable.", 503)
	}
	return l, t.Terminal(), nil
}
