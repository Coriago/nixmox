package task

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"nixmox/internal/domain"
	"nixmox/internal/engine"
	"nixmox/internal/engine/internal/shared"
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

func validateAdapters(factories []engine.LogAdapterFactory) error {
	if len(factories) > 8 {
		return errors.New("too many log adapters")
	}
	seen := map[string]bool{}
	for _, factory := range factories {
		if factory.Name == "" || len(factory.Name) > 128 || factory.New == nil || seen[factory.Name] {
			return errors.New("invalid or duplicate log adapter factory")
		}
		seen[factory.Name] = true
	}
	return nil
}

type taskRunner struct {
	execution *execution
	base      engine.Runner
}

func (r taskRunner) Run(ctx context.Context, plan engine.CommandLine, stdout, stderr io.Writer) (engine.ProcessResult, error) {
	// Sensitive output bypasses retention, parsers AND caller-provided collectors,
	// even when the underlying Runner implementation does not enforce that policy.
	if plan.SensitiveOutput {
		return r.base.Run(ctx, plan, io.Discard, io.Discard)
	}
	session := r.execution.outputSession(plan)
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

type namedAdapter struct {
	name     string
	adapter  engine.LogAdapter
	disabled bool
}

type outputSession struct {
	mu            sync.Mutex
	execution     *execution
	context       engine.AdapterContext
	adapters      []namedAdapter
	failures      map[string]bool
	progressOwner string
	activityLogs  map[domain.ID]string
}

func (e *execution) outputSession(plan engine.CommandLine) *outputSession {
	e.mu.Lock()
	task := e.current
	factories := append([]engine.LogAdapterFactory(nil), e.adapters...)
	e.mu.Unlock()
	var random [16]byte
	session := &outputSession{execution: e, context: engine.AdapterContext{TaskID: task.ID, NodeID: task.OwnerNodeID, Phase: task.Phase}, failures: map[string]bool{}, activityLogs: map[domain.ID]string{}}
	if _, err := rand.Read(random[:]); err != nil {
		session.degrade("adapters")
		return session
	}
	session.context.ProcessID = domain.ID("process_" + hex.EncodeToString(random[:]))
	for _, factory := range factories {
		if factory.Match != nil && !factory.Match(plan) {
			continue
		}
		adapter, err := factory.New(session.context)
		if err != nil || adapter == nil {
			session.degrade(factory.Name)
			continue
		}
		session.adapters = append(session.adapters, namedAdapter{name: factory.Name, adapter: adapter})
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
		event, err := s.execution.scheduler.Store.Append(context.Background(), s.context.TaskID, nil, data[:n])
		if err != nil {
			return written, err
		}
		record := engine.LogRecord{Stream: w.stream, Data: data[:n], Offset: event.Offset, ObservedAt: time.Now().UTC()}
		for i := range s.adapters {
			adapter := &s.adapters[i]
			if adapter.disabled {
				continue
			}
			record.Data = bytes.Clone(data[:n]) // Parsers cannot corrupt raw output or another adapter's input.
			update, parseErr, panicked := invokeAdapter(adapter.adapter, &record)
			if err = s.apply(adapter.name, update); err != nil {
				s.degrade(adapter.name)
			}
			if parseErr != nil {
				s.degrade(adapter.name)
			}
			if panicked {
				adapter.disabled = true
			}
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
	for i := range s.adapters {
		a := &s.adapters[i]
		if a.disabled {
			continue
		}
		update, err, _ := invokeAdapter(a.adapter, nil)
		if saveErr := s.apply(a.name, update); err != nil || saveErr != nil {
			s.degrade(a.name)
		}
	}
	for id, name := range s.activityLogs {
		if err := s.execution.scheduler.Store.Close(context.Background(), s.context.TaskID, &id); err != nil {
			s.degrade(name)
		}
	}
}

func (s *outputSession) activityID(name string, id domain.ID) domain.ID {
	return shared.ID("activity_", domain.ID(string(s.context.ProcessID)+":"+name+":"+string(id)))
}

func (s *outputSession) degrade(name string) {
	if s.failures[name] {
		return
	}
	s.failures[name] = true
	s.execution.scheduler.AdapterFailures.Add(1)
	_ = s.saveProgress(nil) // No parser error strings or raw output enter diagnostics.
}

func (s *outputSession) saveProgress(update *domain.TaskProgress) error {
	j := s.execution.scheduler.Store
	j.mu.Lock()
	defer j.mu.Unlock()
	path := "jobs/" + shared.Filename(s.context.TaskID) + "/progress.json"
	var prior domain.TaskProgress
	if err := j.read(path, &prior); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
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
	for name := range s.failures {
		field := "log_adapter." + name
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
	return j.write(path, progress)
}

func (s *outputSession) apply(name string, update engine.LogUpdate) error {
	if len(update.Activities) > 256 || len(update.Logs) > 256 {
		return errors.New("adapter emitted too many updates")
	}
	j := s.execution.scheduler.Store
	for _, activity := range update.Activities {
		if activity.ID == "" || (activity.TaskID != "" && activity.TaskID != s.context.TaskID) || (activity.NodeID != "" && activity.NodeID != s.context.NodeID) {
			return errors.New("adapter activity owner mismatch")
		}
		activity.ID = s.activityID(name, activity.ID)
		activity.TaskID = s.context.TaskID
		activity.NodeID = s.context.NodeID
		if activity.LogRef != nil {
			if _, exists := s.activityLogs[activity.ID]; !exists {
				if _, err := j.Append(context.Background(), s.context.TaskID, &activity.ID, nil); err != nil {
					return err
				}
				s.activityLogs[activity.ID] = name
			}
			activity.LogRef = shared.Ptr(fmt.Sprintf("/api/v1/tasks/%s/activities/%s/logs", activity.TaskID, activity.ID))
		}
		err := func() error {
			j.mu.Lock()
			defer j.mu.Unlock()
			path := "jobs/" + shared.Filename(activity.TaskID) + "/activities/" + shared.Filename(activity.ID) + ".json"
			var prior domain.Activity
			if err := j.read(path, &prior); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			activity.Version = prior.Version + 1
			return j.write(path, activity)
		}()
		if err != nil {
			return err
		}
	}
	for _, log := range update.Logs {
		if log.ActivityID == "" || len(log.Data) > domain.CommandPayloadLimit {
			return errors.New("invalid activity log")
		}
		id := s.activityID(name, log.ActivityID)
		// Derived logs must refer to an actual activity of this task/process/adapter.
		j.mu.Lock()
		var activity domain.Activity
		err := j.read("jobs/"+shared.Filename(s.context.TaskID)+"/activities/"+shared.Filename(id)+".json", &activity)
		j.mu.Unlock()
		if err != nil {
			return err
		}
		data := log.Data
		for len(data) > 0 {
			n := min(len(data), domain.LogEventLimit/2)
			if _, err = j.Append(context.Background(), s.context.TaskID, &id, data[:n]); err != nil {
				return err
			}
			data = data[n:]
		}
	}
	if update.Progress != nil {
		if s.progressOwner != "" && s.progressOwner != name {
			return errors.New("multiple aggregate progress producers require an explicit reducer")
		}
		s.progressOwner = name
		progress := shared.Clone(*update.Progress)
		if progress.TaskID != "" && progress.TaskID != s.context.TaskID {
			return errors.New("adapter progress owner mismatch")
		}
		for i, id := range progress.ActiveActivityIDs {
			progress.ActiveActivityIDs[i] = s.activityID(name, id)
		}
		return s.saveProgress(&progress)
	}
	return nil
}
