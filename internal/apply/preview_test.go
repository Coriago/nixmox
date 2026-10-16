package apply

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSystemPreviewDoesNotActivate(t *testing.T) {
	fx := newApplyFixture(t, false, "switch")
	root := t.TempDir()
	current := filepath.Join(root, "running")
	candidate := filepath.Join(root, "candidate")
	for _, dir := range []string{current, candidate} {
		if err := os.MkdirAll(filepath.Join(dir, "etc"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(current, "etc", "service.conf"), []byte("old\n"), 0600)
	os.WriteFile(filepath.Join(candidate, "etc", "service.conf"), []byte("new\n"), 0600)
	fx.mgr.currentSystem = current
	bin := t.TempDir()
	script := `#!/bin/sh
if [ "$1" = "build" ]; then
  while [ "$#" -gt 0 ]; do
    if [ "$1" = "--out-link" ]; then shift; ln -s "$PREVIEW_CANDIDATE" "$1"; fi
    shift
  done
else
  echo 'package: 1.0 -> 2.0'
fi
`
	os.WriteFile(filepath.Join(bin, "nix"), []byte(script), 0700)
	t.Setenv("PREVIEW_CANDIDATE", candidate)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	j, err := fx.mgr.Preview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	fx.mgr.runner.Stop()
	result, err := fx.store.Get(j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.State != "applied" || result.Preview == nil {
		t.Fatalf("%+v", result)
	}
	if !strings.Contains(result.Preview.Packages, "1.0 -> 2.0") || !strings.Contains(result.Preview.Configuration, "+new") {
		t.Fatalf("%+v", result.Preview)
	}
	if got := LatestGeneration(fx.profiles); got != 41 {
		t.Fatalf("preview changed generation: %d", got)
	}
	if _, ok, _ := fx.records.Latest("node1"); ok {
		t.Fatal("preview wrote apply record")
	}
	if !strings.Contains(j.Args[len(j.Args)-1], "rev=deadbeef") {
		t.Fatal("preview not pinned")
	}
}
func TestLimitedPreviewReportsTruncation(t *testing.T) {
	b := &limitedBuffer{limit: 4}
	n, err := b.Write([]byte("abcdef"))
	if err != nil || n != 6 || b.String() != "abcd" || !b.truncated {
		t.Fatalf("%+v %d %v", b, n, err)
	}
	b.Write([]byte("more"))
	if b.Len() != 4 {
		t.Fatal(b.Len())
	}
}
