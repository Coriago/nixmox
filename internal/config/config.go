package config

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

// The module and daemon read these same defaults.
//
//go:embed defaults.json
var defaults []byte

//go:embed repository-defaults.json
var repositoryDefaults []byte

func Default() Config {
	var c Config
	if err := json.Unmarshal(defaults, &c); err != nil {
		panic(err)
	}
	return c
}
func DefaultRepository() Repository {
	var r Repository
	if err := json.Unmarshal(repositoryDefaults, &r); err != nil {
		panic(err)
	}
	return r
}

func Load(file string) (Config, error) {
	c := Default()
	if file != "" {
		f, err := os.Open(file)
		if err != nil {
			return c, err
		}
		defer f.Close()
		b, err := io.ReadAll(io.LimitReader(f, 1024*1024+1))
		if err != nil {
			return c, err
		}
		if len(b) > 1024*1024 {
			return c, errors.New("configuration exceeds 1 MiB")
		}
		if err := Decode(b, &c); err != nil {
			return c, err
		}
	}
	return c, c.Validate()
}
func Decode(b []byte, c *Config) error {
	d := yaml.NewDecoder(bytes.NewReader(b))
	d.KnownFields(true)
	if err := d.Decode(c); err != nil {
		return errors.New("invalid configuration: unknown field or invalid value")
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return errors.New("configuration must contain one document")
	}
	// Entry defaults come from the same source consumed by the NixOS module.
	var tree yaml.Node
	if err := yaml.Unmarshal(b, &tree); err != nil {
		return errors.New("invalid configuration")
	}
	present := map[string]map[string]bool{}
	if len(tree.Content) > 0 {
		root := tree.Content[0]
		for i := 0; i+1 < len(root.Content); i += 2 {
			if root.Content[i].Value == "repositories" {
				entries := root.Content[i+1]
				for j := 0; j+1 < len(entries.Content); j += 2 {
					keys := map[string]bool{}
					v := entries.Content[j+1]
					for k := 0; k+1 < len(v.Content); k += 2 {
						keys[v.Content[k].Value] = true
					}
					present[entries.Content[j].Value] = keys
				}
			}
		}
	}
	for name, repo := range c.Repositories {
		if !present[name]["upstream_branch"] {
			repo.UpstreamBranch = DefaultRepository().UpstreamBranch
		}
		c.Repositories[name] = repo
	}
	return nil
}
func validLabel(s string) bool {
	return len(s) > 0 && len(s) <= 256 && utf8.ValidString(s) && !strings.ContainsAny(s, "\x00\r\n")
}
func (c Config) TCPPorts() map[string]int {
	return map[string]int{"http.port": c.HTTP.Port, "machine.port": c.Machine.Port, "nats.client_port": c.NATS.ClientPort, "nats.route_port": c.NATS.RoutePort, "nats.leaf_port": c.NATS.LeafPort}
}
func (c Config) Validate() error {
	if c.SchemaVersion != 1 {
		return errors.New("unsupported schema_version")
	}
	if !filepath.IsAbs(c.DataDir) || strings.ContainsAny(c.DataDir, "\x00\r\n") || strings.HasPrefix(filepath.Clean(c.DataDir), "/nix/store/") {
		return errors.New("data_dir must be an absolute runtime directory")
	}
	used := map[int]string{}
	for key, port := range c.TCPPorts() {
		if port < 1 || port > 65535 {
			return fmt.Errorf("%s must be between 1 and 65535", key)
		}
		if other, ok := used[port]; ok {
			return fmt.Errorf("%s overlaps %s", key, other)
		}
		used[port] = key
	}
	if c.Discovery.Port < 1 || c.Discovery.Port > 65535 {
		return errors.New("discovery.port must be between 1 and 65535")
	}
	seen := map[string]bool{}
	for _, n := range c.Network.ManagementInterfaces {
		if len(n) == 0 || len(n) > 15 || strings.ContainsAny(n, "/\x00\r\n \t:") || seen[n] {
			return errors.New("invalid or duplicate management interface")
		}
		seen[n] = true
	}
	preferred := map[string]bool{}
	for _, n := range c.Network.PreferredInterfaces {
		if !seen[n] || preferred[n] {
			return errors.New("preferred interfaces must be unique management interfaces")
		}
		preferred[n] = true
	}
	if (c.Cluster.Name == nil) != (c.Cluster.JoinAddress == nil) {
		return errors.New("cluster.name and cluster.join_address must both be set")
	}
	if c.Cluster.Name != nil {
		if !validLabel(*c.Cluster.Name) {
			return errors.New("invalid cluster.name")
		}
		h, p, err := net.SplitHostPort(*c.Cluster.JoinAddress)
		n, portErr := strconv.Atoi(p)
		if err != nil || h == "" || portErr != nil || n < 1 || n > 65535 {
			return errors.New("cluster.join_address must be host:port")
		}
	}
	if c.Configuration != nil && (!validLabel(c.Configuration.Name) || !validLabel(c.Configuration.Repository)) {
		return errors.New("configuration requires repository and name")
	}
	for name, repo := range c.Repositories {
		if !validLabel(name) || !validLabel(repo.UpstreamBranch) || strings.ContainsAny(repo.UpstreamBranch, " ~^:?*[\\") || strings.HasPrefix(repo.UpstreamBranch, "-") || strings.Contains(repo.UpstreamBranch, "..") {
			return errors.New("invalid repository name or upstream branch")
		}
		if err := ValidateRemote(repo.RemoteURL); err != nil {
			return err
		}
	}
	for name, credential := range c.Credentials {
		if !validLabel(name) || !CredentialKind(credential.Kind) {
			return errors.New("invalid credential name or kind")
		}
		if !filepath.IsAbs(credential.SecretFile) || strings.HasPrefix(filepath.Clean(credential.SecretFile), "/nix/store/") || strings.ContainsAny(credential.SecretFile, "\x00\r\n") {
			return errors.New("credential secret_file must be a runtime absolute path outside the Nix store")
		}
	}
	if c.Deployment.KeepGenerations < 1 {
		return errors.New("deployment.keep_generations must be positive")
	}
	if c.Cache.MaxConcurrentTransfers < 1 {
		return errors.New("cache.max_concurrent_transfers must be positive")
	}
	fast := c.Telemetry.Host.FastIntervalMs
	fs := c.Telemetry.Host.FilesystemIntervalMs
	metrics := c.Telemetry.OTLP.MetricsIntervalMs
	if fast < 250 || fast > 5000 {
		return errors.New("telemetry.host.fast_interval_ms must be between 250 and 5000")
	}
	if fs < 1000 || fs > 60000 || fs < fast {
		return errors.New("invalid telemetry.host.filesystem_interval_ms")
	}
	if metrics < 250 || metrics > 60000 || metrics < fast {
		return errors.New("invalid telemetry.otlp.metrics_interval_ms")
	}
	if c.Telemetry.OTLP.Enable && c.Telemetry.OTLP.Endpoint == nil {
		return errors.New("telemetry.otlp.endpoint is required")
	}
	if c.Telemetry.OTLP.Endpoint != nil {
		u, err := url.Parse(*c.Telemetry.OTLP.Endpoint)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return errors.New("invalid telemetry.otlp.endpoint")
		}
	}
	for name, profile := range c.Media.Profiles {
		if !validLabel(name) {
			return errors.New("invalid media profile ID")
		}
		if err := profile.Validate(); err != nil {
			return err
		}
	}
	return nil
}
func CredentialKind(kind string) bool {
	switch kind {
	case "git-http-token", "git-ssh-key", "ssh-private-key", "nix-cache-signing-key", "sops-age-key":
		return true
	}
	return false
}
func ValidateRemote(remote string) error {
	if remote == "" || len(remote) > 4096 || strings.ContainsAny(remote, "\x00\r\n") || strings.HasPrefix(remote, "-") {
		return errors.New("invalid repository remote_url")
	}
	if regexp.MustCompile(`^[^/:]+::`).MatchString(remote) {
		return errors.New("Git external transport helpers are not allowed")
	}
	if strings.Contains(remote, "://") {
		u, err := url.Parse(remote)
		if err != nil {
			return errors.New("invalid repository remote_url")
		}
		if u.RawQuery != "" || u.Fragment != "" {
			return errors.New("repository URLs cannot contain query credentials or fragments")
		}
		switch u.Scheme {
		case "https", "http", "ssh", "file":
		default:
			return errors.New("unsupported repository transport")
		}
		if u.User != nil {
			if _, secret := u.User.Password(); secret || u.Scheme != "ssh" {
				return errors.New("repository credentials must use credential_ref")
			}
		}
		if u.Scheme != "file" && u.Host == "" {
			return errors.New("repository remote host is required")
		}
	} else if !filepath.IsAbs(remote) && !regexp.MustCompile(`^[A-Za-z0-9_.-]+@[A-Za-z0-9_.-]+:[^\s]+$`).MatchString(remote) {
		return errors.New("remote_url must be an explicit URL, SSH address or absolute path")
	}
	return nil
}
func (p Profile) Validate() error {
	if !validLabel(p.Label) || !regexp.MustCompile(`^[a-z0-9_]+-linux$`).MatchString(p.System) {
		return errors.New("invalid media profile label or system")
	}
	if p.Format != "iso" && p.Format != "raw" {
		return errors.New("media format must be iso or raw")
	}
	switch p.Compression {
	case "none", "zstd", "xz", "gzip":
	default:
		return errors.New("unsupported media compression")
	}
	if len(p.SupportedBoards) == 0 || len(p.BootMedia) == 0 {
		return errors.New("media requires supported_boards and boot_media")
	}
	for _, v := range p.BootMedia {
		if !validLabel(v) {
			return errors.New("invalid boot media")
		}
	}
	if !validLabel(p.Source.Repository) || !validLabel(p.Source.Ref) || !validLabel(p.Source.FlakeAttribute) {
		return errors.New("media source requires repository, ref and flake_attribute")
	}
	if p.Source.ArtifactPath == "" || path.IsAbs(p.Source.ArtifactPath) || path.Clean(p.Source.ArtifactPath) != p.Source.ArtifactPath || strings.HasPrefix(p.Source.ArtifactPath, "../") || p.Source.ArtifactPath == ".." || strings.ContainsAny(p.Source.ArtifactPath, "\\\x00\r\n") {
		return errors.New("media artifact_path must be a safe relative path")
	}
	return nil
}
