package gitops

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

type mockClient struct {
	remoteSHA  string
	currentSHA string
	fetchErr   error
	cloned     bool
}

func (m *mockClient) Clone(ctx context.Context, url, branch, dir string) error {
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		return err
	}
	m.cloned = true
	return nil
}
func (m *mockClient) Fetch(ctx context.Context, dir string) error { return m.fetchErr }
func (m *mockClient) RemoteHEAD(ctx context.Context, dir, branch string) (string, error) {
	return m.remoteSHA, nil
}
func (m *mockClient) ResetHard(ctx context.Context, dir, branch string) error { return nil }
func (m *mockClient) CurrentSHA(ctx context.Context, dir string) (string, error) {
	return m.currentSHA, nil
}

func TestPoller(t *testing.T) {
	ctx := context.Background()

	t.Run("no change", func(t *testing.T) {
		mc := &mockClient{remoteSHA: "aaa", currentSHA: "aaa"}
		mgr := NewManager(t.TempDir(), mc)
		mgr.Configure("file:///x", "main")
		if _, err := mgr.Ensure(ctx); err != nil {
			t.Fatal(err)
		}
		fired := 0
		p := NewPoller(mgr, func(string) { fired++ })
		p.Tick(ctx)
		if fired != 0 || mgr.State().UpdateAvailable {
			t.Errorf("no-change tick: fired=%d state=%+v", fired, mgr.State())
		}
	})

	t.Run("change detected", func(t *testing.T) {
		mc := &mockClient{remoteSHA: "aaa", currentSHA: "aaa"}
		mgr := NewManager(t.TempDir(), mc)
		mgr.Configure("file:///x", "main")
		if _, err := mgr.Ensure(ctx); err != nil {
			t.Fatal(err)
		}
		var got string
		p := NewPoller(mgr, func(sha string) { got = sha })
		mc.remoteSHA = "bbb"
		p.Tick(ctx)
		if got != "bbb" || !mgr.State().UpdateAvailable {
			t.Errorf("change tick: got=%q state=%+v", got, mgr.State())
		}
	})

	t.Run("initial clone via poll", func(t *testing.T) {
		mc := &mockClient{remoteSHA: "aaa", currentSHA: "aaa"}
		mgr := NewManager(t.TempDir(), mc)
		mgr.Configure("file:///x", "main")
		fired := 0
		p := NewPoller(mgr, func(string) { fired++ })
		p.Tick(ctx)
		if !mc.cloned {
			t.Error("first poll should perform the initial clone")
		}
		if fired != 0 {
			t.Error("initial clone is the baseline, not an update — onNew must not fire")
		}
		if mgr.State().LastPulledSHA != "aaa" {
			t.Errorf("state after initial clone: %+v", mgr.State())
		}
	})

	t.Run("fetch error is non-fatal", func(t *testing.T) {
		mc := &mockClient{remoteSHA: "aaa", currentSHA: "aaa", fetchErr: fmt.Errorf("network down")}
		mgr := NewManager(t.TempDir(), mc)
		mgr.Configure("file:///x", "main")
		mgr.Ensure(ctx)
		fired := 0
		p := NewPoller(mgr, func(string) { fired++ })
		p.Tick(ctx)
		p.Tick(ctx)
		if fired != 0 {
			t.Errorf("error tick should not fire onNew, fired=%d", fired)
		}
	})
}
