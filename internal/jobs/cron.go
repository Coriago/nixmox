package jobs

import (
	"context"

	"github.com/robfig/cron/v3"
)

// Cron wraps robfig/cron/v3 with standard options.
type Cron struct {
	c *cron.Cron
}

// NewCron creates a new cron scheduler.
func NewCron() *Cron {
	c := cron.New(cron.WithChain(
		cron.Recover(cron.DefaultLogger),
		cron.DelayIfStillRunning(cron.DefaultLogger),
	))
	return &Cron{c: c}
}

// AddFunc adds a func to the Cron to be run on the given schedule.
func (c *Cron) AddFunc(spec string, cmd func()) (cron.EntryID, error) {
	return c.c.AddFunc(spec, cmd)
}

// Start starts the cron scheduler in its own goroutine.
func (c *Cron) Start(ctx context.Context) {
	c.c.Start()
	go func() {
		<-ctx.Done()
		c.c.Stop()
	}()
}

// Stop stops the cron scheduler.
func (c *Cron) Stop() {
	c.c.Stop()
}
