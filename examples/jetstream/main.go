// main.go sketches the intended explicit API walkthrough; operations are stubs.
package main

import (
	"context"
	"fmt"
)

// main invokes the walkthrough. Until implementation, the first action reports
// ErrNotImplemented; only the daemon directories are created.
func main() {
	if err := walkthrough(context.Background()); err != nil {
		fmt.Println(err)
	}
}

// walkthrough shows creation, cluster setup, enrollment, promotion, and demotion.
func walkthrough(ctx context.Context) error {
	a, err := NewDaemon("")
	if err != nil {
		return err
	}
	defer a.Close()

	b, err := NewDaemon("")
	if err != nil {
		return err
	}
	defer b.Close()

	c, err := NewDaemon("")
	if err != nil {
		return err
	}
	defer c.Close()

	if err := a.CreateCluster(ctx); err != nil {
		return err
	}

	if err := b.Enroll(ctx, a); err != nil {
		return err
	}
	if err := c.Enroll(ctx, a); err != nil {
		return err
	}

	if err := a.Promote(ctx, b.ID); err != nil {
		return err
	}
	if err := b.Promote(ctx, c.ID); err != nil {
		return err
	}

	// A sends the event; C owns the demotion task.
	if err := a.Demote(ctx, c.ID); err != nil {
		return err
	}

	if err := c.Stop(ctx, false); err != nil {
		return err
	}
	if err := b.Stop(ctx, true); err != nil {
		return err
	}
	return a.Stop(ctx, false)
}
