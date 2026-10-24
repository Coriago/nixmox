package task

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"nixmox/internal/domain"
	"nixmox/internal/engine"
	"nixmox/internal/engine/internal/shared"
	"nixmox/internal/engine/telemetry"
)

// LineAdapter provides bounded per-stream framing for line-oriented parsers.
// It drops oversized records (not the raw task log) and resumes at the next newline.
type LineAdapter struct {
	Limit          int
	Parse          func(engine.LogRecord) (engine.LogUpdate, error)
	stdout, stderr pendingLine
}

type pendingLine struct {
	data     []byte
	offset   int64
	at       time.Time
	dropping bool
}

func mergeLogUpdate(dst *engine.LogUpdate, src engine.LogUpdate) error {
	if len(dst.Activities)+len(src.Activities) > 256 || len(dst.Logs)+len(src.Logs) > 256 {
		return errors.New("adapter update exceeds item bound")
	}
	if src.Progress != nil {
		dst.Progress = src.Progress
	}
	dst.Activities = append(dst.Activities, src.Activities...)
	dst.Logs = append(dst.Logs, src.Logs...)
	return nil
}

func (l *LineAdapter) Consume(record engine.LogRecord) (engine.LogUpdate, error) {
	var result engine.LogUpdate
	pending := &l.stdout
	if record.Stream == engine.Stderr {
		pending = &l.stderr
	} else if record.Stream != engine.Stdout {
		return result, errors.New("unknown output stream")
	}
	limit := l.Limit
	if limit <= 0 {
		limit = domain.CommandPayloadLimit
	}
	var failure error
	data := record.Data
	position := 0
	for len(data) > 0 {
		n := bytes.IndexByte(data, '\n')
		complete := n >= 0
		if !complete {
			n = len(data)
		}
		if len(pending.data) == 0 && !pending.dropping {
			pending.offset = record.Offset + int64(position)
			pending.at = record.ObservedAt
		}
		if !pending.dropping {
			if len(pending.data)+n > limit {
				pending.data = nil
				pending.dropping = true
				failure = errors.New("adapter line exceeds size bound")
			} else {
				pending.data = append(pending.data, data[:n]...)
			}
		}
		consumed := n
		if complete {
			consumed++
		}
		data = data[consumed:]
		position += consumed
		if complete {
			if !pending.dropping {
				update, err := l.Parse(engine.LogRecord{Stream: record.Stream, Data: pending.data, Offset: pending.offset, ObservedAt: pending.at})
				failure = errors.Join(failure, err, mergeLogUpdate(&result, update))
			}
			pending.data = nil
			pending.dropping = false
		}
	}
	return result, failure
}

func (l *LineAdapter) Flush() (engine.LogUpdate, error) {
	var result engine.LogUpdate
	var failure error
	for _, stream := range []engine.OutputStream{engine.Stdout, engine.Stderr} {
		pending := &l.stdout
		if stream == engine.Stderr {
			pending = &l.stderr
		}
		if len(pending.data) > 0 && !pending.dropping {
			update, err := l.Parse(engine.LogRecord{Stream: stream, Data: pending.data, Offset: pending.offset, ObservedAt: pending.at})
			failure = errors.Join(failure, err, mergeLogUpdate(&result, update))
		}
		*pending = pendingLine{}
	}
	return result, failure
}

type taskRunner struct {
	execution *execution
	base      engine.Runner
}

func (r taskRunner) Run(ctx context.Context, plan engine.CommandLine, stdout, stderr io.Writer) (_ engine.ProcessResult, result error) {
	ctx, span := telemetry.Start(ctx, "process.execute")
	defer func() { telemetry.End(span, result) }()
	// Sensitive output bypasses retention, parsers AND caller-provided collectors,
	// even when the underlying Runner implementation does not enforce that policy.
	if plan.SensitiveOutput {
		return r.base.Run(ctx, plan, io.Discard, io.Discard)
	}
	session := r.execution.outputSession(ctx, plan)
	defer session.flush()
	out := session.writer(engine.Stdout)
	errout := session.writer(engine.Stderr)
	if stdout != nil {
		out = io.MultiWriter(out, stdout)
	}
	if stderr != nil {
		errout = io.MultiWriter(errout, stderr)
	}
	return r.base.Run(ctx, plan, out, errout)
}

type outputSession struct {
	traceContext       context.Context
	mu                 sync.Mutex
	execution          *execution
	context            engine.AdapterContext
	adapter            engine.LogAdapter
	name               string
	disabled, degraded bool
	activityLogs       map[domain.ID]bool
}

func (e *execution) outputSession(ctx context.Context, plan engine.CommandLine) *outputSession {
	e.mu.Lock()
	task := e.current
	factory := e.definition.Adapter
	e.mu.Unlock()
	var random [16]byte
	session := &outputSession{execution: e, traceContext: context.WithoutCancel(ctx), context: engine.AdapterContext{TaskID: task.ID, NodeID: task.OwnerNodeID, Phase: task.Phase}, activityLogs: map[domain.ID]bool{}}
	_, err := rand.Read(random[:])
	if err != nil {
		session.degrade()
		return session
	}
	session.context.ProcessID = domain.ID("process_" + hex.EncodeToString(random[:]))
	if factory != nil && (factory.Match == nil || factory.Match(plan)) {
		session.name = factory.Name
		session.adapter, err = factory.New(session.context)
		if err != nil || session.adapter == nil {
			session.degrade()
		}
	}
	return session
}

func (s *outputSession) writer(stream engine.OutputStream) io.Writer {
	return outputWriter{session: s, stream: stream}
}

type outputWriter struct {
	session *outputSession
	stream  engine.OutputStream
}

func (w outputWriter) Write(data []byte) (int, error) {
	s := w.session
	s.mu.Lock()
	defer s.mu.Unlock()
	written := 0
	for len(data) > 0 {
		n := min(len(data), domain.LogEventLimit/2)
		event, err := s.execution.manager.Append(s.traceContext, s.context.TaskID, nil, data[:n])
		if err != nil {
			return written, err
		}
		record := engine.LogRecord{Stream: w.stream, Data: data[:n], Offset: event.Offset, ObservedAt: time.Now().UTC()}
		if s.adapter != nil && !s.disabled {
			record.Data = bytes.Clone(data[:n]) // A parser cannot mutate caller-owned output.
			update, parseErr, panicked := invokeAdapter(s.adapter, &record)
			if err := s.apply(update); err != nil || parseErr != nil {
				s.degrade()
			}
			s.disabled = panicked
		}
		written += n
		data = data[n:]
	}
	return written, nil
}

// Instrumentation bugs cannot panic through os/exec's copy goroutines.
func invokeAdapter(adapter engine.LogAdapter, record *engine.LogRecord) (update engine.LogUpdate, err error, panicked bool) {
	defer func() {
		if recover() != nil {
			update = engine.LogUpdate{}
			err = errors.New("log adapter panicked")
			panicked = true
		}
	}()
	if record == nil {
		update, err = adapter.Flush()
	} else {
		update, err = adapter.Consume(*record)
	}
	return
}

func (s *outputSession) flush() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.adapter != nil && !s.disabled {
		update, err, _ := invokeAdapter(s.adapter, nil)
		if saveErr := s.apply(update); err != nil || saveErr != nil {
			s.degrade()
		}
	}
	for id := range s.activityLogs {
		if err := s.execution.manager.CloseLog(context.Background(), s.context.TaskID, &id); err != nil {
			s.degrade()
		}
	}
}

func (s *outputSession) activityID(id domain.ID) domain.ID {
	return shared.ID("activity_", domain.ID(string(s.context.ProcessID)+":"+s.name+":"+string(id)))
}

func (s *outputSession) degrade() {
	if s.degraded {
		return
	}
	s.degraded = true
	s.execution.manager.AdapterFailures.Add(1)
	_ = s.saveProgress(nil) // No parser error strings or raw output enter diagnostics.
}

func (s *outputSession) saveProgress(update *domain.TaskProgress) error {
	e := s.execution
	e.mu.Lock()
	prior := shared.Clone(e.progress)
	e.mu.Unlock()
	progress := prior
	if update != nil {
		progress = shared.Clone(*update)
	}
	progress.SchemaVersion = domain.SchemaVersion
	progress.TaskID = s.context.TaskID
	progress.Version = prior.Version + 1
	progress.Phase = s.context.Phase
	progress.ObservedAt = shared.Ptr(time.Now().UTC())
	if progress.Freshness == "" {
		progress.Freshness = domain.Live
	}
	if s.degraded {
		field := "log_adapter." + s.name
		found := false
		for _, issue := range progress.Unavailable {
			if issue.Field == field {
				found = true
			}
		}
		if !found {
			progress.Unavailable = append(progress.Unavailable, domain.MeasurementError{Field: field, Reason: "Adapter telemetry unavailable or degraded"})
		}
		progress.Freshness = domain.Unknown
	}
	return e.Progress(s.traceContext, progress)
}

func (s *outputSession) apply(update engine.LogUpdate) error {
	if len(update.Activities) > 256 || len(update.Logs) > 256 {
		return errors.New("adapter emitted too many updates")
	}
	j := s.execution.manager
	for _, activity := range update.Activities {
		if activity.ID == "" || (activity.TaskID != "" && activity.TaskID != s.context.TaskID) || (activity.NodeID != "" && activity.NodeID != s.context.NodeID) {
			return errors.New("adapter activity owner mismatch")
		}
		activity.ID = s.activityID(activity.ID)
		activity.TaskID = s.context.TaskID
		activity.NodeID = s.context.NodeID
		if activity.LogRef != nil {
			if _, exists := s.activityLogs[activity.ID]; !exists {
				if _, err := j.Append(s.traceContext, s.context.TaskID, &activity.ID, nil); err != nil {
					return err
				}
				s.activityLogs[activity.ID] = true
			}
			activity.LogRef = shared.Ptr(fmt.Sprintf("/api/v1/tasks/%s/activities/%s/logs", activity.TaskID, activity.ID))
		}
		e := s.execution
		e.mu.Lock()
		if len(e.activities) >= 64 && e.activities[activity.ID].ID == "" {
			e.mu.Unlock()
			return errors.New("activity retention bound reached")
		}
		activity.Version = e.activities[activity.ID].Version + 1
		e.activities[activity.ID] = shared.Clone(activity)
		e.mu.Unlock()
	}
	for _, log := range update.Logs {
		if log.ActivityID == "" || len(log.Data) > domain.CommandPayloadLimit {
			return errors.New("invalid activity log")
		}
		id := s.activityID(log.ActivityID)
		// Derived logs must refer to an actual activity of this task/process/adapter.
		_, err := j.Activity(s.traceContext, s.context.TaskID, id)
		if err != nil {
			return err
		}
		data := log.Data
		for len(data) > 0 {
			n := min(len(data), domain.LogEventLimit/2)
			if _, err = j.Append(s.traceContext, s.context.TaskID, &id, data[:n]); err != nil {
				return err
			}
			data = data[n:]
		}
	}
	if update.Progress != nil {
		progress := shared.Clone(*update.Progress)
		if progress.TaskID != "" && progress.TaskID != s.context.TaskID {
			return errors.New("adapter progress owner mismatch")
		}
		for i, id := range progress.ActiveActivityIDs {
			progress.ActiveActivityIDs[i] = s.activityID(id)
		}
		return s.saveProgress(&progress)
	}
	return nil
}
