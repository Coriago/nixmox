package task

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"nixmox/internal/domain"
	"nixmox/internal/engine"
	"nixmox/internal/engine/internal/shared"
	"nixmox/internal/engine/internal/testutil"
)

func TestTaskOwnsGenericLogAdapters(t *testing.T) {
	var instances atomic.Int32
	adapter := engine.LogAdapterFactory{Name: "example.counter", New: func(ctx engine.AdapterContext) (engine.LogAdapter, error) {
		instances.Add(1)
		return &LineAdapter{Limit: 64, Parse: func(record engine.LogRecord) (engine.LogUpdate, error) {
			if record.Stream != engine.Stdout {
				return engine.LogUpdate{}, nil
			}
			if string(record.Data) == "panic" {
				panic("untrusted parser details")
			}
			value, err := strconv.ParseUint(string(record.Data), 10, 64)
			if err != nil {
				return engine.LogUpdate{}, errors.New("invalid metric")
			}
			return engine.LogUpdate{Progress: &domain.TaskProgress{TaskID: ctx.TaskID, Downloads: domain.DownloadProgress{BytesReceived: &value}}, Activities: []domain.Activity{{ID: "activity", Name: "sample", State: "running", LogRef: shared.Ptr("local")}}, Logs: []engine.ActivityLog{{ActivityID: "activity", Data: []byte("derived\n")}}}, nil
		}}, nil
	}}
	base := testutil.RunnerFunc(func(_ context.Context, _ engine.CommandLine, out, errout io.Writer) (engine.ProcessResult, error) {
		// stdout framing must not consume interleaved stderr as part of its line.
		for _, write := range []struct {
			w    io.Writer
			data string
		}{{out, "1"}, {errout, "stderr\n"}, {out, "2\ninvalid\n13"}} {
			if _, err := io.WriteString(write.w, write.data); err != nil {
				return engine.ProcessResult{}, err
			}
		}
		return engine.ProcessResult{ExitCode: 0}, nil
	})
	def := engine.TaskDefinition{Adapter: &adapter, Phase: "run", Interruptible: true, Run: func(ctx context.Context, e engine.Execution) error {
		for range 2 {
			result, err := e.Runner(base).Run(ctx, engine.CommandLine{Program: "fake"}, nil, nil)
			if err != nil || result.ExitCode != 0 {
				return fmt.Errorf("adapter changed command result: %v", err)
			}
		}
		return nil
	}}
	scheduler := NewManager(context.Background(), "node")
	defer scheduler.Close()
	receipt, err := scheduler.Accept(context.Background(), localEnvelope("adapters"), def, acknowledged)
	if err != nil {
		t.Fatal(err)
	}
	task := awaitTask(t, scheduler, *receipt.TaskID)
	if instances.Load() != 2 || scheduler.AdapterFailures.Load() != 2 {
		t.Fatalf("instances=%d failures=%d", instances.Load(), scheduler.AdapterFailures.Load())
	}
	if task.State != domain.Succeeded || task.Version != 3 {
		t.Fatalf("telemetry changed lifecycle: %+v", task)
	}
	progress, err := scheduler.Progress(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if progress.Downloads.BytesReceived == nil || *progress.Downloads.BytesReceived != 13 || progress.Freshness != domain.Unknown {
		t.Fatalf("EOF flush/degradation: %+v", progress)
	}
	_, r, err := scheduler.Snapshot(context.Background(), task.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(r)
	r.Close()
	if string(raw) != strings.Repeat("1stderr\n2\ninvalid\n13", 2) {
		t.Fatalf("raw log changed: %q", raw)
	}
	execution, _ := scheduler.find(task.ID)
	execution.mu.Lock()
	activities := maps.Clone(execution.activities)
	execution.mu.Unlock()
	if len(activities) != 2 {
		t.Fatal("activity identities collided")
	}
	for _, activity := range activities {
		if activity.TaskID != task.ID || activity.NodeID != "node" || !strings.Contains(*activity.LogRef, string(activity.ID)) {
			t.Fatal("invalid activity ownership/link")
		}
		chunk, err := scheduler.Read(context.Background(), task.ID, &activity.ID, 0, 1024)
		if err != nil || chunk.NextOffset != 16 || !chunk.Complete {
			t.Fatalf("derived logs: %+v %v", chunk, err)
		}
	}
}

func TestLineAdapterBoundsAndPanics(t *testing.T) {
	var lines []string
	adapter := &LineAdapter{Limit: 4, Parse: func(r engine.LogRecord) (engine.LogUpdate, error) {
		lines = append(lines, string(r.Data))
		return engine.LogUpdate{}, nil
	}}
	_, err := adapter.Consume(engine.LogRecord{Stream: engine.Stdout, Data: []byte("12345\nok\ntail"), ObservedAt: time.Now()})
	if err == nil {
		t.Fatal("oversized line accepted")
	}
	if _, err = adapter.Flush(); err != nil {
		t.Fatal(err)
	}
	if strings.Join(lines, ",") != "ok,tail" {
		t.Fatal(lines)
	}
	crashing := &LineAdapter{Parse: func(engine.LogRecord) (engine.LogUpdate, error) { panic("sensitive panic text") }}
	_, err, panicked := invokeAdapter(crashing, &engine.LogRecord{Stream: engine.Stderr, Data: []byte("event\n")})
	if err == nil || !panicked || strings.Contains(err.Error(), "sensitive") {
		t.Fatal("panic was not isolated and sanitized")
	}
}

func TestSensitiveOutputSkipsAdapters(t *testing.T) {
	called := false
	session := taskRunner{base: testutil.RunnerFunc(func(_ context.Context, _ engine.CommandLine, out, stderr io.Writer) (engine.ProcessResult, error) {
		called = true
		out.Write([]byte("secret"))
		stderr.Write([]byte("secret"))
		return engine.ProcessResult{ExitCode: 0}, nil
	})}
	// No execution is needed: sensitive output never even constructs adapters.
	var out bytes.Buffer
	result, err := session.Run(context.Background(), engine.CommandLine{SensitiveOutput: true}, &out, &out)
	if err != nil || !called || out.Len() != 0 || result.ExitCode != 0 {
		t.Fatal("sensitive output reached a consumer", err)
	}
}
