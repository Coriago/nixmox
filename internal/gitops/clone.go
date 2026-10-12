package gitops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"nixmox/internal/statefile"
	"os"
	"path/filepath"
	"sync"
)

type State struct {
	RemoteSHA       string `json:"remote_sha"`
	RepoURL         string `json:"repo_url"`
	Branch          string `json:"branch"`
	LastPulledSHA   string `json:"last_pulled_sha"`
	UpdateAvailable bool   `json:"update_available"`
}

type Manager struct {
	dir       string
	statePath string
	client    Client

	// opMu serializes checkout operations and state writers; mu keeps State()
	// responsive while Git is waiting on a network or an SSH server.
	opMu  sync.Mutex
	mu    sync.Mutex
	state State
}

func NewManager(dataDir string, client Client) *Manager {
	return &Manager{
		dir:       filepath.Join(dataDir, "git", "repo"),
		statePath: filepath.Join(dataDir, "git", "state.json"),
		client:    client,
	}
}

func (m *Manager) Load() error {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	data, err := os.ReadFile(m.statePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read gitops state: %w", err)
	}
	return json.Unmarshal(data, &m.state)
}

func (m *Manager) Configure(url, branch string) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	if branch == "" {
		branch = "main"
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state.RepoURL != "" && m.state.RepoURL != url {
		if _, err := os.Stat(filepath.Join(m.dir, ".git")); err == nil {
			return fmt.Errorf("cannot change repository URL while a checkout exists; stop the daemon and archive git/repo first")
		}
	}
	m.state.RepoURL = url
	m.state.Branch = branch
	return m.persistLocked()
}

func (m *Manager) State() State {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state
}

func (m *Manager) Ensure(ctx context.Context) (string, error) {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	return m.ensureLocked(ctx)
}

func (m *Manager) ensureLocked(ctx context.Context) (string, error) {
	if m.state.RepoURL == "" {
		return "", fmt.Errorf("gitops repo URL not configured")
	}
	if err := os.MkdirAll(filepath.Dir(m.dir), 0o750); err != nil {
		return "", err
	}

	if _, err := os.Stat(filepath.Join(m.dir, ".git")); errors.Is(err, os.ErrNotExist) {
		if err := m.client.Clone(ctx, m.state.RepoURL, m.state.Branch, m.dir); err != nil {
			return "", fmt.Errorf("clone %s: %w", m.state.RepoURL, err)
		}
	} else {
		if err := m.client.Fetch(ctx, m.dir); err != nil {
			return "", fmt.Errorf("fetch: %w", err)
		}
		if err := m.client.ResetHard(ctx, m.dir, m.state.Branch); err != nil {
			return "", fmt.Errorf("reset: %w", err)
		}
	}

	sha, err := m.client.CurrentSHA(ctx, m.dir)
	if err != nil {
		return "", fmt.Errorf("read HEAD after pull: %w", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.state.LastPulledSHA = sha
	m.state.UpdateAvailable = false
	return sha, m.persistLocked()
}

func (m *Manager) CheckRemote(ctx context.Context) (bool, string, error) {
	m.opMu.Lock()
	defer m.opMu.Unlock()

	if m.state.RepoURL == "" {
		return false, "", nil
	}
	if _, err := os.Stat(filepath.Join(m.dir, ".git")); errors.Is(err, os.ErrNotExist) {
		sha, err := m.ensureLocked(ctx)
		return false, sha, err
	}

	if err := m.client.Fetch(ctx, m.dir); err != nil {
		return false, "", fmt.Errorf("fetch: %w", err)
	}
	remote, err := m.client.RemoteHEAD(ctx, m.dir, m.state.Branch)
	if err != nil {
		return false, "", fmt.Errorf("resolve origin/%s: %w", m.state.Branch, err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.state.RemoteSHA = remote
	if remote != m.state.LastPulledSHA {
		m.state.UpdateAvailable = true
		return true, remote, m.persistLocked()
	}
	m.state.UpdateAvailable = false
	return false, remote, m.persistLocked()
}

func (m *Manager) persistLocked() error {
	return statefile.Write(m.statePath, m.state)
}
