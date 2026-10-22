package task

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"syscall"
	"time"

	"nixmox/internal/engine"
)

var _ engine.Runner = ProcessRunner{}

type ProcessRunner struct {
	Path        string
	Environment map[string]string
	Grace       time.Duration
}

func (r ProcessRunner) Run(ctx context.Context, plan engine.CommandLine, stdout, stderr io.Writer) (engine.ProcessResult, error) {
	result := engine.ProcessResult{ExitCode: -1}
	if plan.Program == "" || strings.IndexByte(plan.Program, 0) >= 0 {
		return result, errors.New("invalid program")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	if plan.SensitiveOutput {
		stdout, stderr = io.Discard, io.Discard
	}
	cmd := exec.CommandContext(ctx, plan.Program, plan.Args...)
	cmd.Dir, cmd.Stdout, cmd.Stderr, cmd.Stdin = plan.Directory, stdout, stderr, plan.Stdin
	env := map[string]string{"PATH": r.Path, "LANG": "C.UTF-8", "LC_ALL": "C.UTF-8"}
	if env["PATH"] == "" {
		env["PATH"] = os.Getenv("PATH")
	}
	for k, v := range r.Environment {
		env[k] = v
	}
	for k, v := range plan.Environment {
		env[k] = v
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if strings.ContainsAny(k, "=\x00") || strings.ContainsRune(env[k], 0) {
			return result, errors.New("invalid process environment")
		}
		cmd.Env = append(cmd.Env, k+"="+env[k])
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	grace := r.Grace
	if grace <= 0 {
		grace = 2 * time.Second
	}
	cmd.WaitDelay = grace
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		if err == syscall.ESRCH {
			return os.ErrProcessDone
		}
		return err
	}
	result.StartedAt = time.Now().UTC()
	if err := cmd.Start(); err != nil {
		return result, err
	}
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		select {
		case <-done:
			return
		case <-ctx.Done():
		}
		timer := time.NewTimer(grace)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-done:
		}
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}()
	err := cmd.Wait()
	close(done)
	<-stopped
	// A completed command cannot leave owned descendants running after exclusions release.
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	result.FinishedAt = time.Now().UTC()
	result.ExitCode = cmd.ProcessState.ExitCode()
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	return result, err
}

// BoundedBuffer is for machine-readable tool metadata, not unbounded task logs.
type BoundedBuffer struct {
	bytes.Buffer
	Limit int
}

func (b *BoundedBuffer) Write(p []byte) (int, error) {
	if len(p) > b.Limit-b.Len() {
		return 0, errors.New("tool output exceeds bound")
	}
	return b.Buffer.Write(p)
}

func Capture(ctx context.Context, r engine.Runner, plan engine.CommandLine, limit int) ([]byte, error) {
	out := &BoundedBuffer{Limit: limit}
	_, err := r.Run(ctx, plan, out, io.Discard)
	if err != nil {
		return nil, fmt.Errorf("%s failed: %w", plan.Program, err)
	}
	return out.Bytes(), nil
}
