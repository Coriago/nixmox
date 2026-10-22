package process

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRealProcessBoundsCancellationAndEnvironment(t *testing.T) {
	t.Setenv("NIXMOX_TEST_PRIVATE", "do-not-inherit")
	r, err := Run(context.Background(), Options{MaxOutput: 1024}, "sh", "-c", `printf '%s' "${NIXMOX_TEST_PRIVATE-unset}"`)
	if err != nil || string(r.Stdout) != "unset" {
		t.Fatal("ambient secret environment inherited", err)
	}
	r, err = Run(context.Background(), Options{MaxOutput: 64}, "sh", "-c", "while :; do printf 1234567890; done")
	if !errors.Is(err, ErrOutputLimit) || len(r.Stdout) != 64 {
		t.Fatal("output was not bounded", len(r.Stdout), err)
	}
	start := time.Now()
	_, err = Run(context.Background(), Options{Timeout: 100 * time.Millisecond}, "sh", "-c", "sleep 30 & wait")
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 2*time.Second {
		t.Fatal("process group cancellation did not bound inherited pipes", err)
	}
}
func TestStreamingProtocolDoesNotAccumulateStderr(t *testing.T) {
	count := 0
	r, err := Run(context.Background(), Options{MaxOutput: 64, Timeout: time.Second, StderrLine: func(line []byte) {
		if string(line) != "record" {
			t.Errorf("invalid record %q", line)
		}
		count++
	}}, "sh", "-c", `i=0; while [ "$i" -lt 512 ]; do printf 'record\n' >&2; i=$((i+1)); done; printf record >&2; printf done`)
	if err != nil || string(r.Stdout) != "done" || len(r.Stderr) != 0 || count != 513 {
		t.Fatal("stream retention or EOF handling failed", count, r, err)
	}
}
