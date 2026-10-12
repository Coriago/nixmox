package gitops

import (
	"context"
	"strings"
	"testing"
)

func TestPreviewAvailableRevision(t *testing.T) {
	bare, work := seedRepo(t)
	m := NewManager(t.TempDir(), ExecClient{})
	ctx := context.Background()
	if err := m.Configure(bare, "main"); err != nil {
		t.Fatal(err)
	}
	base, err := m.Ensure(ctx)
	if err != nil {
		t.Fatal(err)
	}
	commitAndPush(t, work, "hardware.nix")
	changed, target, err := m.CheckRemote(ctx)
	if err != nil || !changed {
		t.Fatal(changed, err)
	}
	p, err := m.Preview(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	if p.Base != base || p.Target != target || !strings.Contains(p.Files, "hardware.nix") {
		t.Fatal(p)
	}
	if m.State().LastPulledSHA != base {
		t.Fatal("preview changed the apply revision")
	}
	if _, err = m.Preview(ctx, "--help"); err == nil {
		t.Fatal("unsafe revision accepted")
	}
	if err = m.Configure("/different-repository", "main"); err == nil {
		t.Fatal("silently reused wrong checkout")
	}
}
