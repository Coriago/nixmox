package controllers

import (
	"context"
	"errors"
	"nixmox/examples/jetstream/stores"
)

type Controller interface {
	ID() string
	Interests() []stores.ID
	Step(context.Context, stores.Snapshots)
	Stop(context.Context) error
}

// One named async function owned by a controller.
type Job struct {
	cancel context.CancelFunc
	done   bool
	err    error
}

func (j *Job) Result() (bool, error) {
	// Report whether the function has exited and its result.
	return false, errors.ErrUnsupported
}

type Base struct {
	HostID string
	Wake   func()
	jobs   map[string]*Job
}

func NewBase(hostID string, wake func()) Base {
	return Base{HostID: hostID, Wake: wake, jobs: make(map[string]*Job)}
}

func (c *Base) StartJob(ctx context.Context, name string, run func(context.Context) error) {
	// Register the named function and run it asynchronously.
	// Do not duplicate an existing job. Different jobs may run concurrently.
	// Track cancellation, completion and error; wake this controller when it finishes.
}

func (c *Base) StopJob(name string) {
	// Request cancellation. Keep tracking the job until its function exits.
}

func (c *Base) Job(name string) *Job {
	// Look up the job so Step can inspect its progress/result.
	return nil
}

func (c *Base) ForgetJob(name string) {
	// Remove a completed job after the controller has handled its result.
	// Never remove work that is still running.
}

func (c *Base) Stop(ctx context.Context) error {
	// Cancel and wait for this controller's jobs after dispatching has stopped.
	return errors.ErrUnsupported
}
