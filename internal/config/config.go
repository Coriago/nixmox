// Package config loads nixmox configuration from defaults, file, and environment
// variables with the precedence: defaults < config file < env vars.
//
// Config file format is YAML (chosen for human readability in homelab settings;
// TOML would also work, but YAML is the de-facto standard for this ecosystem).
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Version constants are injected at build time via ldflags.
var (
	Version = "dev"
	Commit  = "none"
)

// Config holds all nixmox runtime settings.
type Config struct {
	DataDir     string `yaml:"data_dir"`
	HTTPAddr    string `yaml:"http_addr"`
	NATSPort    int    `yaml:"nats_port"`
	ClusterPort int    `yaml:"cluster_port"`
	LogLevel    string `yaml:"log_level"`

	GitRepoURL      string `yaml:"git_repo_url"`
	GitBranch       string `yaml:"git_branch"`
	GitPollInterval int    `yaml:"git_poll_interval_seconds"`
	SSHKeyPath      string `yaml:"git_ssh_key_path"`
	NetrcPath       string `yaml:"git_netrc_path"`

	AllowConfigWrite bool `yaml:"allow_config_write"`
}

// DefaultConfig returns the built-in defaults.
func DefaultConfig() Config {
	return Config{
		DataDir:         "/var/lib/nixmox",
		HTTPAddr:        "127.0.0.1:8080",
		NATSPort:        4222,
		ClusterPort:     6222,
		LogLevel:        "info",
		GitBranch:       "main",
		GitPollInterval: 60,
	}
}

// Load reads configuration with precedence: defaults < file < env vars.
// The configFile path can be empty; if so, no file is loaded.
func Load(configFile string) (*Config, error) {
	cfg := DefaultConfig()

	if configFile != "" {
		if err := loadFile(configFile, &cfg); err != nil && !(os.IsNotExist(err) && configFile == FindConfigFile("")) {
			return nil, fmt.Errorf("load config file %q: %w", configFile, err)
		}
	}

	loadEnv(&cfg)

	return &cfg, nil
}

func loadFile(path string, cfg *Config) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return yaml.Unmarshal(data, cfg)
}

const envPrefix = "NIXMOX_"

func loadEnv(cfg *Config) {
	if v := os.Getenv(envPrefix + "DATA_DIR"); v != "" {
		cfg.DataDir = v
	}
	if v := os.Getenv(envPrefix + "HTTP_ADDR"); v != "" {
		cfg.HTTPAddr = v
	}
	if v := os.Getenv(envPrefix + "NATS_PORT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.NATSPort = n
		}
	}
	if v := os.Getenv(envPrefix + "CLUSTER_PORT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.ClusterPort = n
		}
	}
	if v := os.Getenv(envPrefix + "LOG_LEVEL"); v != "" {
		cfg.LogLevel = v
	}
	if v := os.Getenv(envPrefix + "GIT_REPO_URL"); v != "" {
		cfg.GitRepoURL = v
	}
	if v := os.Getenv(envPrefix + "GIT_BRANCH"); v != "" {
		cfg.GitBranch = v
	}
	if v := os.Getenv(envPrefix + "GIT_POLL_INTERVAL_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.GitPollInterval = n
		}
	}
	if v := os.Getenv(envPrefix + "GIT_SSH_KEY_PATH"); v != "" {
		cfg.SSHKeyPath = v
	}
	if v := os.Getenv(envPrefix + "GIT_NETRC_PATH"); v != "" {
		cfg.NetrcPath = v
	}
	if v := os.Getenv(envPrefix + "ALLOW_CONFIG_WRITE"); v != "" {
		cfg.AllowConfigWrite = v == "true" || v == "1" || v == "yes"
	}
}

// FindConfigFile searches for a config file in standard locations.
// Precedence: explicit path > NIXMOX_CONFIG env > /etc/nixmox/config.yaml.
func FindConfigFile(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if v := os.Getenv("NIXMOX_CONFIG"); v != "" {
		return v
	}
	return "/etc/nixmox/config.yaml"
}

// String returns a human-readable summary of the resolved config.
func (c *Config) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "data_dir=%s http_addr=%s nats_port=%d cluster_port=%d log_level=%s",
		c.DataDir, c.HTTPAddr, c.NATSPort, c.ClusterPort, c.LogLevel)
	return b.String()
}

// EnsureDataDir creates the data directory if it does not exist.
func (c *Config) EnsureDataDir() error {
	return os.MkdirAll(c.DataDir, 0o750)
}

// DataDirPath returns an absolute path under the data directory.
func (c *Config) DataDirPath(parts ...string) string {
	return filepath.Join(append([]string{c.DataDir}, parts...)...)
}
