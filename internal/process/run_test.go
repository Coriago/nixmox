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
