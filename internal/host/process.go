// Package host executes host-owned operations with explicit argument vectors.
package host

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"syscall"
	"time"

	"go.opentelemetry.io/otel"
)

// Process never logs arguments or stdin: either may contain credentials. The
// caller supplies an appropriately redacting log sink for process output.
type Process struct {
	Log io.Writer
	// Env replaces, rather than extends, the environment when non-nil.
	// Secret-handling subprocesses use this to avoid ambient credentials/config.
	Env []string
}

func (p Process) Run(ctx context.Context, dir string, stdin io.Reader, name string, args ...string) ([]byte, error) {
	ctx, span := otel.Tracer("nixmox.host").Start(ctx, "host.process")
	defer span.End()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	command := exec.CommandContext(ctx, name, args...)
	command.Dir = dir
	command.Env = p.Env
	command.Stdin = stdin
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}
	command.WaitDelay = 5 * time.Second
	output := &boundedOutput{limit: 8 << 20, cancel: cancel}
	command.Stdout = output
	command.Stderr = p.Log
	if command.Stderr == nil {
		command.Stderr = io.Discard
	}
	err := command.Run()
	if output.overflow {
		return nil, errors.New("process output exceeds 8 MiB")
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return output.Bytes(), err
}

type boundedOutput struct {
	bytes.Buffer
	limit    int
	overflow bool
	cancel   context.CancelFunc
}

func (b *boundedOutput) Write(data []byte) (int, error) {
	if len(data) > b.limit-b.Len() {
		b.overflow = true
		b.cancel()
		return 0, errors.New("output limit exceeded")
	}
	return b.Buffer.Write(data)
}
