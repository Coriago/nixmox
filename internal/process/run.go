// Package process runs external tools with bounded output and cancellation of
// the entire process group. Callers parse output; it is never logged implicitly.
package process

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

var ErrOutputLimit = errors.New("external tool output limit exceeded")

type Options struct {
	MaxOutput   int
	Directory   string
	Environment []string
	Timeout     time.Duration
}
type Result struct {
	Stdout, Stderr []byte
	ExitCode       int
}
type boundedWriter struct {
	buffer    bytes.Buffer
	remaining int
	overflow  bool
	cancel    context.CancelFunc
}

func (w *boundedWriter) Write(p []byte) (int, error) {
	n := len(p)
	keep := min(n, w.remaining)
	w.buffer.Write(p[:keep])
	w.remaining -= keep
	if n > keep {
		w.overflow = true
		w.cancel()
	}
	return n, nil
}
func Run(ctx context.Context, options Options, program string, args ...string) (Result, error) {
	if options.MaxOutput == 0 {
		options.MaxOutput = 4 << 20
	}
	if options.MaxOutput < 1 || options.MaxOutput > 64<<20 {
		return Result{}, ErrOutputLimit
	}
	if options.Timeout <= 0 {
		options.Timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, options.Timeout)
	defer cancel()
	out := &boundedWriter{remaining: options.MaxOutput, cancel: cancel}
	stderr := &boundedWriter{remaining: options.MaxOutput, cancel: cancel}
	cmd := exec.CommandContext(ctx, program, args...)
	cmd.Dir = options.Directory
	cmd.Stdout = out
	cmd.Stderr = stderr
	cmd.WaitDelay = time.Second
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	for _, name := range []string{"PATH", "HOME", "USER", "TMPDIR", "XDG_RUNTIME_DIR"} {
		if value, ok := os.LookupEnv(name); ok {
			cmd.Env = append(cmd.Env, name+"="+value)
		}
	}
	cmd.Env = append(cmd.Env, "LANG=C.UTF-8", "LC_ALL=C.UTF-8")
	cmd.Env = append(cmd.Env, options.Environment...)
	err := cmd.Run()
	result := Result{Stdout: out.buffer.Bytes(), Stderr: stderr.buffer.Bytes(), ExitCode: -1}
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}
	if out.overflow || stderr.overflow {
		return result, ErrOutputLimit
	}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	return result, err
}
