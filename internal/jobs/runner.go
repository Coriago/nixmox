package jobs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os/exec"
	"sort"
	"sync"
	"syscall"
	"time"
)

// Action runs within the host queue. It may enrich the persisted job metadata.
// Register actions at composition time; transports only validate and submit jobs.
type Action func(context.Context, *Job, io.Writer) error

type queuedJob struct {
	ctx context.Context
	job Job
}

type jobControl struct {
	ctx    context.Context
	cancel context.CancelCauseFunc
	stop   func() bool
}

var ErrCancelled = errors.New("cancelled by user")
var ErrNotCancellable = errors.New("task is no longer cancellable")

type Runner struct {
	controls   map[string]*jobControl
	store      Store
	mu         sync.Mutex
	submitMu   sync.Mutex
	queues     map[string][]queuedJob
	actions    map[string]Action
	wg         sync.WaitGroup
	ctx        context.Context
	cancel     context.CancelFunc
	closed     bool
	OnTerminal func(Job)
}

func NewRunner(store Store) *Runner {
	ctx, cancel := context.WithCancel(context.Background())
	r := &Runner{store: store, controls: make(map[string]*jobControl), queues: make(map[string][]queuedJob), actions: make(map[string]Action), ctx: ctx, cancel: cancel, OnTerminal: func(Job) {}}
	for kind, command := range map[string]string{"echo": "echo", "shutdown": "shutdown", "reboot": "shutdown", "provision": "nixos-anywhere"} {
		r.Register(kind, Command(command))
	}
	r.Register("command", func(ctx context.Context, j *Job, out io.Writer) error {
		if j.Command == "" {
			return fmt.Errorf("command is required")
		}
		return RunCommand(ctx, j, out, j.Command, j.Args...)
	})
	for _, kind := range []string{"apply", "rollback", "dry-build"} {
		r.Register(kind, func(ctx context.Context, j *Job, out io.Writer) error {
			mode := j.Mode
			if j.Kind == "dry-build" {
				mode = "dry-build"
			}
			if mode == "" {
				mode = "switch"
			}
			return RunCommand(ctx, j, out, "nixos-rebuild", append([]string{mode}, j.Args...)...)
		})
	}
	return r
}

func (r *Runner) Register(kind string, action Action) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.actions[kind] = action
}

func Command(command string) Action {
	return func(ctx context.Context, j *Job, out io.Writer) error {
		return RunCommand(ctx, j, out, command, j.Args...)
	}
}

// RunCommand is shared by built-in and custom actions. No shell is implicit.
// A process group ensures cancellation reaches child processes as well.
func RunCommand(ctx context.Context, j *Job, out io.Writer, command string, args ...string) error {
	fmt.Fprintf(out, "$ %s %q\n", command, args)
	cmd := CommandContext(ctx, command, args...)
	cmd.Dir = j.Directory
	cmd.Stdout, cmd.Stderr = out, out
	err := cmd.Run()
	code := 0
	if err != nil {
		code = -1
		var e *exec.ExitError
		if errors.As(err, &e) {
			code = e.ExitCode()
		}
	}
	j.ExitCode = &code
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

// CommandContext terminates a command's process group on cancellation, including
// helpers such as Git's SSH transport and shell-script children.
func CommandContext(ctx context.Context, command string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	return cmd
}

// Start enqueues a persisted job. Each host has one FIFO worker.
func (r *Runner) Start(ctx context.Context, j Job) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return fmt.Errorf("runner is shutting down")
	}
	if _, exists := r.controls[j.ID]; exists {
		return fmt.Errorf("job already queued")
	}
	jobCtx, cancel := context.WithCancelCause(r.ctx)
	stop := context.AfterFunc(ctx, func() { cancel(ctx.Err()) })
	if ctx.Err() != nil {
		cancel(ctx.Err())
	}
	r.controls[j.ID] = &jobControl{jobCtx, cancel, stop}
	_, running := r.queues[j.Host]
	r.queues[j.Host] = append(r.queues[j.Host], queuedJob{jobCtx, j})
	if !running {
		r.wg.Add(1)
		go r.drain(j.Host)
	}
	return nil
}

func (r *Runner) drain(host string) {
	defer r.wg.Done()
	for {
		r.mu.Lock()
		queue := r.queues[host]
		if len(queue) == 0 || r.closed {
			delete(r.queues, host)
			r.mu.Unlock()
			return
		}
		next := queue[0]
		r.queues[host] = queue[1:]
		action := r.actions[next.job.Kind]
		r.mu.Unlock()
		r.run(next.ctx, next.job, action)
	}
}

type jobWriter struct {
	store Store
	id    string
}

func (w jobWriter) Write(p []byte) (int, error) {
	if err := w.store.AppendLog(w.id, string(p)); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (r *Runner) run(ctx context.Context, j Job, action Action) {
	if ctx.Err() != nil {
		r.failJob(j, ctx.Err())
		return
	}
	j.State = "building"
	j.StartedAt = time.Now().UTC()
	j.UpdatedAt = j.StartedAt
	if err := r.store.Update(j); err != nil {
		r.failJob(j, fmt.Errorf("persist running state: %w", err))
		return
	}
	var err error
	if action == nil {
		err = fmt.Errorf("unknown job kind: %s", j.Kind)
	} else {
		err = action(ctx, &j, jobWriter{r.store, j.ID})
	}
	if err != nil {
		r.failJob(j, err)
		return
	}
	j.State = "applied"
	r.finish(j)
}

func (r *Runner) failJob(j Job, err error) {
	j.Error = err.Error()
	if e := r.store.AppendLog(j.ID, "\n"+err.Error()+"\n"); e != nil {
		log.Printf("job %s log: %v", j.ID, e)
	}
	j.State = "failed"
	r.finish(j)
}
func (r *Runner) finish(j Job) {
	r.mu.Lock()
	if control := r.controls[j.ID]; control != nil {
		if errors.Is(context.Cause(control.ctx), ErrCancelled) {
			j.State = "cancelled"
			j.Error = ErrCancelled.Error()
			j.Phase = "Cancelled; completed effects are not undone"
		}
		control.stop()
		control.cancel(nil)
		delete(r.controls, j.ID)
	}

	j.UpdatedAt = time.Now().UTC()
	if err := r.store.Update(j); err != nil {
		log.Printf("job %s persist terminal state: %v", j.ID, err)
		r.mu.Unlock()
		return
	}
	r.mu.Unlock()
	log.Printf("job %s (%s on %s): %s %s", j.ID, j.Kind, j.Host, j.State, j.Error)
	r.OnTerminal(j)
}

// Recover resumes only jobs that never started. Interrupted commands are never
// retried automatically because their external effects may already have occurred.
func (r *Runner) Recover() error {
	list, err := r.store.List()
	if err != nil {
		return err
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].CreatedAt.Before(list[j].CreatedAt) })
	for _, j := range list {
		switch j.State {
		case "cancelling":
			j.State = "cancelled"
			j.Error = "cancellation interrupted by daemon restart; inspect effects before retrying"
			r.finish(j)
		case "building", "applying":
			r.failJob(j, fmt.Errorf("daemon interrupted this job; inspect effects before retrying"))
		case "pending":
			if j.Kind == "installer-write" || j.Kind == "host-install" || j.Kind == "host-onboard" {
				r.failJob(j, fmt.Errorf("destructive task requires fresh confirmation after restart"))
				continue
			}
			if err := r.Start(context.Background(), j); err != nil {
				return err
			}
		}
	}
	return nil
}

// Stop waits for the queue to drain (used by batch callers and tests).
func (r *Runner) Stop() { r.wg.Wait() }

// Shutdown cancels running commands and leaves pending jobs durable for restart.
func (r *Runner) Shutdown() {
	r.mu.Lock()
	r.closed = true
	r.cancel()
	r.mu.Unlock()
	r.wg.Wait()
}

// Cancel removes queued work or signals the active action. Completion is reported
// only after the action returns, including any remote cancellation cleanup.
func (r *Runner) Cancel(id string) (Job, error) {
	r.submitMu.Lock()
	defer r.submitMu.Unlock()
	r.mu.Lock()
	j, err := r.store.Get(id)
	if err != nil {
		r.mu.Unlock()
		return Job{}, err
	}
	if j.State == "cancelled" || j.State == "cancelling" {
		r.mu.Unlock()
		return j, nil
	}
	control := r.controls[id]
	if control == nil {
		r.mu.Unlock()
		return j, ErrNotCancellable
	}
	j.State = "cancelling"
	j.UpdatedAt = time.Now().UTC()
	if err := r.store.Update(j); err != nil {
		r.mu.Unlock()
		return j, err
	}
	control.cancel(ErrCancelled)
	queued := false
	for i, next := range r.queues[j.Host] {
		if next.job.ID == id {
			r.queues[j.Host] = append(r.queues[j.Host][:i], r.queues[j.Host][i+1:]...)
			queued = true
			break
		}
	}
	r.mu.Unlock()
	_ = r.store.AppendLog(id, "\nCancellation requested; completed effects are not undone.\n")
	if queued {
		r.finish(j)
	}
	return r.store.Get(id)
}
