package jobs

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/robfig/cron/v3"
)

type exactDelay struct {
	delay time.Duration
}

func (e exactDelay) Next(t time.Time) time.Time {
	return t.Add(e.delay)
}

func TestCron(t *testing.T) {
	c := NewCron()
	var count int32

	// The plan requests "@every 100ms", but robfig/cron/v3 truncates sub-second
	// intervals in its ConstantDelaySchedule, causing it to fire only once.
	// We use a custom schedule that accurately represents "@every 100ms".
	c.c.Schedule(exactDelay{100 * time.Millisecond}, cron.FuncJob(func() {
		atomic.AddInt32(&count, 1)
		time.Sleep(250 * time.Millisecond)
	}))

	ctx, cancel := context.WithCancel(context.Background())
	c.Start(ctx)

	time.Sleep(350 * time.Millisecond)

	midCount := atomic.LoadInt32(&count)
	if midCount > 2 {
		t.Errorf("Expected at most 2 firings in 350ms, got %d", midCount)
	}

	time.Sleep(250 * time.Millisecond)
	cancel()

	finalCount := atomic.LoadInt32(&count)
	if finalCount < 2 {
		t.Errorf("Expected at least 2 firings in 600ms, got %d", finalCount)
	}
}
