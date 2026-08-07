package jobs

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"
)

// ErrHostBusy is returned when a job is already running for the host.
var ErrHostBusy = errors.New("host is busy")

// Runner executes jobs in goroutines.
type Runner struct {
	store      Store
	mu         sync.Map
	wg         sync.WaitGroup
	OnTerminal func(job Job)
}

// NewRunner creates a new job runner.
func NewRunner(store Store) *Runner {
	return &Runner{
		store:      store,
		OnTerminal: func(job Job) {},
	}
}

// Start begins execution of a job in a goroutine.
func (r *Runner) Start(ctx context.Context, j Job) error {
	v, _ := r.mu.LoadOrStore(j.Host, &sync.Mutex{})
	hostMu := v.(*sync.Mutex)

	if !hostMu.TryLock() {
		return ErrHostBusy
	}

	j.State = "building"
	j.UpdatedAt = time.Now().Truncate(time.Millisecond)
	if err := r.store.Update(j); err != nil {
		hostMu.Unlock()
		return fmt.Errorf("update job state: %w", err)
	}

	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		defer hostMu.Unlock()

		r.run(ctx, j)
	}()

	return nil
}

func (r *Runner) run(ctx context.Context, j Job) {
	var cmd *exec.Cmd
	switch j.Kind {
	case "echo":
		cmd = exec.CommandContext(ctx, "echo", j.Args...)
	case "dry-build":
		cmd = exec.CommandContext(ctx, "nixos-rebuild", append([]string{"dry-build"}, j.Args...)...)
	case "apply", "rollback":
		mode := j.Mode
		if mode == "" {
			mode = "switch"
		}
		cmd = exec.CommandContext(ctx, "nixos-rebuild", append([]string{mode}, j.Args...)...)
	default:
		r.failJob(j, fmt.Errorf("unknown job kind: %s", j.Kind))
		return
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		r.failJob(j, fmt.Errorf("stdout pipe: %w", err))
		return
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		r.failJob(j, fmt.Errorf("stderr pipe: %w", err))
		return
	}

	if err := cmd.Start(); err != nil {
		r.failJob(j, fmt.Errorf("start command: %w", err))
		return
	}

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		r.streamLog(j.ID, stdout)
	}()

	go func() {
		defer wg.Done()
		r.streamLog(j.ID, stderr)
	}()

	wg.Wait()

	err = cmd.Wait()
	if err != nil {
		if ctx.Err() != nil {
			r.store.AppendLog(j.ID, "context canceled\n")
		}
		r.failJob(j, err)
		return
	}

	j.State = "applied"
	j.UpdatedAt = time.Now().Truncate(time.Millisecond)
	r.store.Update(j)
	r.OnTerminal(j)
}

func (r *Runner) streamLog(id string, rdr io.Reader) {
	scanner := bufio.NewScanner(rdr)
	for scanner.Scan() {
		r.store.AppendLog(id, scanner.Text()+"\n")
	}
}

func (r *Runner) failJob(j Job, err error) {
	r.store.AppendLog(j.ID, err.Error()+"\n")
	j.State = "failed"
	j.UpdatedAt = time.Now().Truncate(time.Millisecond)
	r.store.Update(j)
	r.OnTerminal(j)
}

// Stop waits for all running jobs to finish.
func (r *Runner) Stop() {
	r.wg.Wait()
}
