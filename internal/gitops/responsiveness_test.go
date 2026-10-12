package gitops

import (
	"context"
	"testing"
	"time"
)

type slowClient struct {
	mockClient
	started, release chan struct{}
}

func (c *slowClient) Fetch(ctx context.Context, dir string) error {
	close(c.started)
	select {
	case <-c.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func TestStateResponsiveDuringFetch(t *testing.T) {
	c := &slowClient{mockClient: mockClient{currentSHA: "abc", remoteSHA: "abc"}, started: make(chan struct{}), release: make(chan struct{})}
	m := NewManager(t.TempDir(), c)
	if err := m.Configure("fixture", "main"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := m.Ensure(context.Background()); done <- err }()
	<-c.started
	state := make(chan State, 1)
	go func() { state <- m.State() }()
	select {
	case <-state:
	case <-time.After(time.Second):
		close(c.release)
		t.Fatal("status blocked behind Git network request")
	}
	close(c.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
