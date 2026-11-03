package util

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
)

func ExampleConfigure() {
	// --- Test setup ---
	previous := slog.Default()
	defer func() {
		Configure(io.Discard, slog.DiscardHandler, nil)
		slog.SetDefault(previous)
	}()

	// Use a text exporter without timestamps so the example output is stable.
	export := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		ReplaceAttr: func(_ []string, attr slog.Attr) slog.Attr {
			if attr.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return attr
		},
	})

	// --- Usage ---
	Configure(io.Discard, export, []string{"demo-key"})

	// Attach operation identity once; subsequent logs inherit it.
	ctx := With(context.Background(), "operation", "install", "node", "worker-1")
	From(ctx).Info("starting installation")
	From(ctx).Warn("rejected demo-key", "token", "demo-key")

	// --- Assertions: go test checks the exact output below ---

	// Output:
	// level=INFO msg="starting installation" operation=install node=worker-1
	// level=WARN msg="rejected [redacted]" operation=install node=worker-1 token=[redacted]
}

func ExampleError() {
	// --- Test setup ---
	previous := slog.Default()
	defer func() {
		Configure(io.Discard, slog.DiscardHandler, nil)
		slog.SetDefault(previous)
	}()
	Configure(io.Discard, slog.DiscardHandler, []string{"demo-key"})

	// --- Usage ---
	// Sanitize an error before sending it to telemetry.
	err := Error(errors.New("authentication failed with demo-key"))

	// --- Assertions: go test checks the exact output below ---
	fmt.Println(err)

	// Output: authentication failed with [redacted]
}
