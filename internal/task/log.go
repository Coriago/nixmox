package task

import (
	"bytes"
	"context"
	"errors"
	"time"

	collectlogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	common "go.opentelemetry.io/proto/otlp/common/v1"
	logs "go.opentelemetry.io/proto/otlp/logs/v1"

	"nixmox/internal/util"
)

// LogWriter frames subprocess stderr into retained task logs. Writes are bounded
// and synchronous: no unbounded queue can outlive the task or lose its tail.
type LogWriter struct {
	ctx       context.Context
	execution *Execution
	pending   []byte
}

func (e *Execution) LogWriter(ctx context.Context) *LogWriter {
	return &LogWriter{ctx: ctx, execution: e}
}

func (w *LogWriter) Write(data []byte) (int, error) {
	written := len(data)
	for len(data) > 0 {
		end := bytes.IndexByte(data, '\n')
		if end < 0 {
			end = len(data)
		}
		if len(w.pending)+end > 64<<10 {
			return 0, errors.New("task log line exceeds 64 KiB")
		}
		w.pending = append(w.pending, data[:end]...)
		data = data[end:]
		if len(data) == 0 {
			break
		}
		if err := w.flush(); err != nil {
			return 0, err
		}
		data = data[1:]
	}
	return written, nil
}

func (w *LogWriter) Close() error {
	if len(w.pending) == 0 {
		return nil
	}
	return w.flush()
}

func (w *LogWriter) flush() error {
	manager := w.execution.manager
	if manager.Logs.JS == nil {
		return errors.New("task log store unavailable")
	}
	// Preserve the Nix JSON envelope while applying configured secret redaction.
	line := util.Error(errors.New(string(w.pending))).Error()
	payload := &collectlogs.ExportLogsServiceRequest{
		ResourceLogs: []*logs.ResourceLogs{{ScopeLogs: []*logs.ScopeLogs{{
			LogRecords: []*logs.LogRecord{{
				TimeUnixNano: uint64(time.Now().UnixNano()),
				Body:         &common.AnyValue{Value: &common.AnyValue_StringValue{StringValue: line}},
			}},
		}}}},
	}
	_, err := manager.Logs.Append(w.ctx, "task", w.execution.Task().ID, payload)
	w.pending = w.pending[:0]
	return err
}
