package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.DataDir != "/var/lib/nixmox" {
		t.Errorf("DataDir default: got %q, want %q", cfg.DataDir, "/var/lib/nixmox")
	}
	if cfg.HTTPAddr != "127.0.0.1:8080" {
		t.Errorf("HTTPAddr default: got %q, want %q", cfg.HTTPAddr, "127.0.0.1:8080")
	}
	if cfg.NATSPort != 4222 {
		t.Errorf("NATSPort default: got %d, want %d", cfg.NATSPort, 4222)
	}
	if cfg.ClusterPort != 6222 {
		t.Errorf("ClusterPort default: got %d, want %d", cfg.ClusterPort, 6222)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("LogLevel default: got %q, want %q", cfg.LogLevel, "info")
	}
}

func TestLoadFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := `data_dir: /tmp/nixmox
http_addr: 0.0.0.0:9090
nats_port: 5222
cluster_port: 7222
log_level: debug
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write config file: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if cfg.DataDir != "/tmp/nixmox" {
		t.Errorf("DataDir: got %q, want %q", cfg.DataDir, "/tmp/nixmox")
	}
	if cfg.HTTPAddr != "0.0.0.0:9090" {
		t.Errorf("HTTPAddr: got %q, want %q", cfg.HTTPAddr, "0.0.0.0:9090")
	}
	if cfg.NATSPort != 5222 {
		t.Errorf("NATSPort: got %d, want %d", cfg.NATSPort, 5222)
	}
	if cfg.ClusterPort != 7222 {
		t.Errorf("ClusterPort: got %d, want %d", cfg.ClusterPort, 7222)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel: got %q, want %q", cfg.LogLevel, "debug")
	}
}

func TestLoadEnv(t *testing.T) {
	t.Setenv("NIXMOX_DATA_DIR", "/env/nixmox")
	t.Setenv("NIXMOX_HTTP_ADDR", "0.0.0.0:7070")
	t.Setenv("NIXMOX_NATS_PORT", "9999")
	t.Setenv("NIXMOX_CLUSTER_PORT", "8888")
	t.Setenv("NIXMOX_LOG_LEVEL", "warn")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if cfg.DataDir != "/env/nixmox" {
		t.Errorf("DataDir: got %q, want %q", cfg.DataDir, "/env/nixmox")
	}
	if cfg.HTTPAddr != "0.0.0.0:7070" {
		t.Errorf("HTTPAddr: got %q, want %q", cfg.HTTPAddr, "0.0.0.0:7070")
	}
	if cfg.NATSPort != 9999 {
		t.Errorf("NATSPort: got %d, want %d", cfg.NATSPort, 9999)
	}
	if cfg.ClusterPort != 8888 {
		t.Errorf("ClusterPort: got %d, want %d", cfg.ClusterPort, 8888)
	}
	if cfg.LogLevel != "warn" {
		t.Errorf("LogLevel: got %q, want %q", cfg.LogLevel, "warn")
	}
}

func TestLoadPrecedence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := `data_dir: /file/nixmox
http_addr: 0.0.0.0:6060
nats_port: 1111
cluster_port: 2222
log_level: error
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write config file: %v", err)
	}

	t.Setenv("NIXMOX_DATA_DIR", "/env/nixmox")
	t.Setenv("NIXMOX_LOG_LEVEL", "debug")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if cfg.DataDir != "/env/nixmox" {
		t.Errorf("DataDir (env override): got %q, want %q", cfg.DataDir, "/env/nixmox")
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel (env override): got %q, want %q", cfg.LogLevel, "debug")
	}

	if cfg.HTTPAddr != "0.0.0.0:6060" {
		t.Errorf("HTTPAddr (file): got %q, want %q", cfg.HTTPAddr, "0.0.0.0:6060")
	}
	if cfg.NATSPort != 1111 {
		t.Errorf("NATSPort (file): got %d, want %d", cfg.NATSPort, 1111)
	}
	if cfg.ClusterPort != 2222 {
		t.Errorf("ClusterPort (file): got %d, want %d", cfg.ClusterPort, 2222)
	}
}

func TestFindConfigFile(t *testing.T) {
	tests := []struct {
		name     string
		explicit string
		env      string
		want     string
	}{
		{
			name:     "explicit wins",
			explicit: "/custom/config.yaml",
			env:      "/env/config.yaml",
			want:     "/custom/config.yaml",
		},
		{
			name:     "env fallback",
			explicit: "",
			env:      "/env/config.yaml",
			want:     "/env/config.yaml",
		},
		{
			name:     "default fallback",
			explicit: "",
			env:      "",
			want:     "/etc/nixmox/config.yaml",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.env != "" {
				t.Setenv("NIXMOX_CONFIG", tt.env)
			}
			got := FindConfigFile(tt.explicit)
			if got != tt.want {
				t.Errorf("FindConfigFile() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestConfigString(t *testing.T) {
	cfg := Config{
		DataDir:     "/var/lib/nixmox",
		HTTPAddr:    "127.0.0.1:8080",
		NATSPort:    4222,
		ClusterPort: 6222,
		LogLevel:    "info",
	}
	got := cfg.String()
	want := "data_dir=/var/lib/nixmox http_addr=127.0.0.1:8080 nats_port=4222 cluster_port=6222 log_level=info"
	if got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

func TestEnsureDataDir(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{DataDir: filepath.Join(dir, "nixmox", "data")}
	if err := cfg.EnsureDataDir(); err != nil {
		t.Fatalf("EnsureDataDir: %v", err)
	}
	if _, err := os.Stat(cfg.DataDir); err != nil {
		t.Errorf("data dir not created: %v", err)
	}
}

func TestDataDirPath(t *testing.T) {
	cfg := Config{DataDir: "/var/lib/nixmox"}
	got := cfg.DataDirPath("jobs", "abc.json")
	want := filepath.Join("/var/lib/nixmox", "jobs", "abc.json")
	if got != want {
		t.Errorf("DataDirPath() = %q, want %q", got, want)
	}
}
