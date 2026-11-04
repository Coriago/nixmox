package cluster_test

import (
	"context"
	"errors"
	"testing"
	"time"

	collectlogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	common "go.opentelemetry.io/proto/otlp/common/v1"
	logs "go.opentelemetry.io/proto/otlp/logs/v1"
	"nixmox/internal/cluster"
	"nixmox/internal/testutil"
)

func TestLogReplayThenLive(t *testing.T) {
	// --- Test setup ---
	// ------------------
	js := testutil.JetStream(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	retained := cluster.Logs{JS: js}
	if err := retained.Provision(ctx, 1); err != nil {
		t.Fatal(err)
	}
	appendLog := func(body string) uint64 {
		t.Helper()
		payload := &collectlogs.ExportLogsServiceRequest{ResourceLogs: []*logs.ResourceLogs{{ScopeLogs: []*logs.ScopeLogs{{LogRecords: []*logs.LogRecord{{Body: &common.AnyValue{Value: &common.AnyValue_StringValue{StringValue: body}}}}}}}}}
		sequence, err := retained.Append(ctx, "task", "cedar.build1", payload)
		if err != nil {
			t.Fatal(err)
		}
		return sequence
	}
	first := appendLog("building")
	second := appendLog("built")

	// --- Usage ---
	// -------------
	history, err := retained.Read(ctx, "task", "cedar.build1", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	done := errors.New("received live event")
	var sequences []uint64
	var live uint64
	err = retained.Follow(ctx, "task", "cedar.build1", first, func(entry cluster.LogEntry) error {
		sequences = append(sequences, entry.Sequence)
		if entry.Sequence == second {
			live = appendLog("activated")
			return nil
		}
		return done
	})

	// --- Assertions ---
	// ------------------
	if !errors.Is(err, done) {
		t.Fatal(err)
	}
	if len(history) != 2 || history[0].Sequence != first || history[1].Sequence != second {
		t.Fatal("historical logs lost")
	}
	if len(sequences) != 2 || sequences[0] != second || sequences[1] != live {
		t.Fatal("gap or duplicate at history/live boundary")
	}
}
