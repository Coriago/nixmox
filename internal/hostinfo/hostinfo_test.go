package hostinfo

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"nixmox/internal/config"
)

func TestGet(t *testing.T) {
	tmpDir := t.TempDir()

	mockOSRelease := filepath.Join(tmpDir, "os-release")
	os.WriteFile(mockOSRelease, []byte("ID=nixos\nVERSION=\"23.11\"\n"), 0644)

	mockUptime := filepath.Join(tmpDir, "uptime")
	os.WriteFile(mockUptime, []byte("12345.67 890.12\n"), 0644)

	oldOSReleasePath := osReleasePath
	oldUptimePath := uptimePath
	defer func() {
		osReleasePath = oldOSReleasePath
		uptimePath = oldUptimePath
	}()

	osReleasePath = mockOSRelease
	uptimePath = mockUptime

	cfg := &config.Config{}
	info := Get(cfg)

	if info.NixOSVersion != "23.11" {
		t.Errorf("expected NixOSVersion '23.11', got %q", info.NixOSVersion)
	}

	expectedUptime, _ := time.ParseDuration("12345.67s")
	if info.Uptime != expectedUptime {
		t.Errorf("expected Uptime %v, got %v", expectedUptime, info.Uptime)
	}

	if info.AgentVersion != config.Version {
		t.Errorf("expected AgentVersion %q, got %q", config.Version, info.AgentVersion)
	}
}

func TestGet_NonNixOS(t *testing.T) {
	tmpDir := t.TempDir()

	mockOSRelease := filepath.Join(tmpDir, "os-release")
	os.WriteFile(mockOSRelease, []byte("ID=ubuntu\nVERSION=\"22.04\"\n"), 0644)

	oldOSReleasePath := osReleasePath
	defer func() { osReleasePath = oldOSReleasePath }()
	osReleasePath = mockOSRelease

	cfg := &config.Config{}
	info := Get(cfg)

	if info.NixOSVersion != "unknown" {
		t.Errorf("expected NixOSVersion 'unknown', got %q", info.NixOSVersion)
	}
}
