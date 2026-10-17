package gitops

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// PinInstall uses the installer's own GitOps pull and read credentials, then
// isolates the exact published commit from subsequent polling and branch changes.
func (m *Manager) PinInstall(ctx context.Context, revision, directory string) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	if !regexp.MustCompile(`^[0-9a-f]{40,64}$`).MatchString(revision) {
		return fmt.Errorf("invalid install revision")
	}
	if _, err := m.ensureLocked(ctx); err != nil {
		return err
	}
	exec, ok := m.client.(ExecClient)
	if !ok {
		return fmt.Errorf("isolated Git checkout unavailable")
	}
	if _, err := os.Stat(directory); err == nil {
		sha, err := exec.CurrentSHA(ctx, directory)
		if err == nil && sha == revision {
			return nil
		}
		return fmt.Errorf("incomplete installation checkout; inspect it before retrying")
	}
	if err := os.MkdirAll(filepath.Dir(directory), 0700); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(filepath.Dir(directory), ".pull-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	checkout := filepath.Join(staging, "repo")
	if _, err := exec.run(ctx, "", "clone", "--no-hardlinks", "--no-checkout", "--", m.dir, checkout); err != nil {
		return err
	}
	if _, err := exec.run(ctx, checkout, "checkout", "--detach", revision); err != nil {
		return err
	}
	sha, err := exec.CurrentSHA(ctx, checkout)
	if err != nil {
		return err
	}
	if strings.TrimSpace(sha) != revision {
		return fmt.Errorf("pulled revision mismatch")
	}
	return os.Rename(checkout, directory)
}
