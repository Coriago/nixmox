package apply

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/url"
	"nixmox/internal/jobs"
	"os"
	"os/exec"
	"path/filepath"
)

// preview realizes the same pinned source as apply, but never activates it.
func (m *Manager) preview(ctx context.Context, j *jobs.Job, out io.Writer) error {
	dir, err := os.MkdirTemp(m.cfg.DataDir, "preview-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	if len(j.Args) == 0 {
		return fmt.Errorf("missing preview flake reference")
	}
	ref, err := url.Parse(j.Args[len(j.Args)-1])
	if err != nil || ref.Fragment == "" {
		return fmt.Errorf("missing configuration attribute")
	}
	ref.Fragment = "nixosConfigurations." + fmt.Sprintf("%q", ref.Fragment) + ".config.system.build.toplevel"
	j.Phase = "Building candidate system; no activation"
	if err := m.store.Update(*j); err != nil {
		return err
	}
	current, err := filepath.EvalSymlinks(m.currentSystem)
	if err != nil {
		return fmt.Errorf("running NixOS system unavailable: %w", err)
	}
	link := filepath.Join(dir, "system")
	if err := jobs.RunCommand(ctx, j, out, "nix", "build", "--impure", "--no-update-lock-file", "--out-link", link, ref.String()); err != nil {
		return err
	}
	candidate, err := filepath.EvalSymlinks(link)
	if err != nil {
		return err
	}
	j.Phase = "Comparing packages and system configuration"
	if err := m.store.Update(*j); err != nil {
		return err
	}
	fmt.Fprintf(out, "\nRunning: %s\nCandidate (%s): %s\n\nPackage closure changes:\n", current, j.GitSHA, candidate)
	j.Preview = &jobs.SystemPreview{Running: current, Candidate: candidate}
	packages := &limitedBuffer{limit: 256 << 10}
	if err := jobs.RunCommand(ctx, j, io.MultiWriter(out, packages), "nix", "store", "diff-closures", current, candidate); err != nil {
		return err
	}
	j.Preview.Packages = packages.String()
	configuration := &limitedBuffer{limit: 512 << 10}
	fmt.Fprintln(out, "\nSystem configuration changes (including systemd units):")
	// diff -r follows the store-backed configuration links. Exit 1 means changes.
	err = jobs.RunCommand(ctx, j, io.MultiWriter(out, configuration), "diff", "-ru", "--", filepath.Join(current, "etc"), filepath.Join(candidate, "etc"))
	if e, ok := err.(*exec.ExitError); ok && e.ExitCode() == 1 {
		err = nil
		code := 0
		j.ExitCode = &code
	}
	if err != nil {
		return err
	}
	j.Preview.Configuration = configuration.String()
	j.Preview.Truncated = packages.truncated || configuration.truncated
	j.Phase = "Preview ready; system was not activated"
	fmt.Fprintln(out, "\nPreview compares against the running system. Activation scripts and runtime service effects cannot be fully predicted. Re-run after changing inputs or pulling another revision.")
	return nil
}

// Keep structured previews bounded; the complete command output remains in the job log.
type limitedBuffer struct {
	bytes.Buffer
	limit     int
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	keep := min(n, b.limit-b.Len())
	b.Buffer.Write(p[:keep])
	if keep < n {
		b.truncated = true
	}
	return n, nil
}
