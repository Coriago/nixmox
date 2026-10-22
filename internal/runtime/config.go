package runtime

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

//go:embed defaults.json
var defaultsJSON []byte

type defaults struct {
	Runtime    Config     `json:"runtime"`
	Repository Repository `json:"repository"`
}

func defaultValues() defaults {
	var d defaults
	if err := json.Unmarshal(defaultsJSON, &d); err != nil {
		panic(err)
	}
	return d
}
func Defaults() Config { return defaultValues().Runtime }

// Load overlays YAML on compiled defaults. Explicit CLI overrides run afterward.
// Secret files are runtime paths; this parser never reads their contents.
func Load(input io.Reader, override func(*Config) error) (Config, error) {
	d := defaultValues()
	c := d.Runtime
	if input != nil {
		decoder := yaml.NewDecoder(input)
		decoder.KnownFields(true)
		if err := decoder.Decode(&c); err != nil {
			return c, fmt.Errorf("configuration: %w", err)
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			return c, fmt.Errorf("configuration must contain one YAML document")
		}
	}
	for name, repo := range c.Repositories {
		if repo.UpstreamBranch == "" {
			repo.UpstreamBranch = d.Repository.UpstreamBranch
		}
		c.Repositories[name] = repo
	}
	if override != nil {
		if err := override(&c); err != nil {
			return c, err
		}
	}
	return c, c.Validate()
}
func (c Config) Validate() error {
	fail := func(key, message string) error { return fmt.Errorf("%s: %s", key, message) }
	if c.SchemaVersion != 1 {
		return fail("schema_version", "only version 1 is supported")
	}
	if !filepath.IsAbs(c.DataDir) {
		return fail("data_dir", "must be an absolute runtime path")
	}
	ports := []struct {
		key   string
		value int
	}{
		{"http.port", c.HTTP.Port}, {"nats.client_port", c.NATS.ClientPort},
		{"nats.route_port", c.NATS.RoutePort}, {"nats.leaf_port", c.NATS.LeafPort},
	}
	used := map[int]string{}
	for _, p := range ports {
		if p.value < 1 || p.value > 65535 {
			return fail(p.key, "must be in 1..65535")
		}
		if prior, ok := used[p.value]; ok {
			return fail(p.key, "overlaps "+prior)
		}
		used[p.value] = p.key
	}
	if c.Discovery.Port < 1 || c.Discovery.Port > 65535 {
		return fail("discovery.port", "must be in 1..65535")
	}
	if (c.Cluster.Name == nil) != (c.Cluster.JoinAddress == nil) {
		return fail("cluster", "name and join_address must be set together")
	}
	if c.Cluster.Name != nil {
		if strings.TrimSpace(*c.Cluster.Name) == "" {
			return fail("cluster.name", "must not be empty")
		}
		if _, _, err := net.SplitHostPort(*c.Cluster.JoinAddress); err != nil {
			return fail("cluster.join_address", "expected host:port")
		}
	}
	for _, iface := range c.Network.PreferredInterfaces {
		if !slices.Contains(c.Network.ManagementInterfaces, iface) {
			return fail("network.preferred_interfaces", "must be allowed management interfaces")
		}
	}
	if c.Deployment.KeepGenerations < 1 {
		return fail("deployment.keep_generations", "must be positive")
	}
	if c.Cache.MaxConcurrentTransfers < 1 {
		return fail("cache.max_concurrent_transfers", "must be positive")
	}
	fast, fs, export := c.Telemetry.Host.FastIntervalMS, c.Telemetry.Host.FilesystemIntervalMS, c.Telemetry.OTLP.MetricsIntervalMS
	if fast < 250 || fast > 5000 {
		return fail("telemetry.host.fast_interval_ms", "must be in 250..5000")
	}
	if fs < 1000 || fs > 60000 || fs < fast {
		return fail("telemetry.host.filesystem_interval_ms", "must be in 1000..60000 and at least fast_interval_ms")
	}
	if export < 250 || export > 60000 || export < fast {
		return fail("telemetry.otlp.metrics_interval_ms", "must be in 250..60000 and at least fast_interval_ms")
	}
	if c.Telemetry.OTLP.Enable {
		if c.Telemetry.OTLP.Endpoint == nil {
			return fail("telemetry.otlp.endpoint", "required when enabled")
		}
		u, err := url.Parse(*c.Telemetry.OTLP.Endpoint)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return fail("telemetry.otlp.endpoint", "expected HTTP(S) URL")
		}
	}
	for name, repo := range c.Repositories {
		if name == "" || strings.TrimSpace(repo.RemoteURL) == "" {
			return fail("repositories", "name and remote_url are required")
		}
	}
	for name, credential := range c.Credentials {
		if name == "" || !filepath.IsAbs(credential.SecretFile) {
			return fail("credentials", "name and absolute runtime secret_file are required")
		}
		if !slices.Contains([]string{"git-http-token", "git-ssh-key", "ssh-private-key", "nix-cache-signing-key", "sops-age-key"}, credential.Kind) {
			return fail("credentials."+name+".kind", "unsupported credential kind")
		}
	}
	if c.Configuration != nil && (c.Configuration.Repository == "" || c.Configuration.Name == "") {
		return fail("configuration", "repository and name are required")
	}
	return nil
}

// JSON is also valid YAML and is used by the module to avoid a second serializer.
func (c Config) JSON() ([]byte, error)  { return json.MarshalIndent(c, "", "  ") }
func Parse(data []byte) (Config, error) { return Load(bytes.NewReader(data), nil) }

// Endpoint precedence is shared by CLI and TUI; remote selection never supplies
// an implicit target host.
func Endpoint(explicit, environment string, local *Config) string {
	if explicit != "" {
		return explicit
	}
	if environment != "" {
		return environment
	}
	if local == nil {
		c := Defaults()
		local = &c
	}
	return fmt.Sprintf("http://127.0.0.1:%d", local.HTTP.Port)
}
