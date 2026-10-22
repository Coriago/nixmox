package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfiguration(t *testing.T) {
	for name, body := range map[string]string{
		"unknown":   "schema_version: 1\nhttp:\n  secret: token\n",
		"zero port": "http:\n  port: 0\n",
		"duplicate": "http:\n  port: 1\n  port: 2\n",
		"multi doc": "schema_version: 1\n---\nschema_version: 1\n",
		"version":   "schema_version: 2\n",
		"relative":  "data_dir: relative\n",
		"preferred": "network:\n  preferred_interfaces: [eth0]\n",
	} {
		t.Run(name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(p, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(p); err == nil {
				t.Fatal("accepted invalid config")
			}
		})
	}
	p := filepath.Join(t.TempDir(), "config.yaml")
	_ = os.WriteFile(p, []byte("http:\n  port: 63200\nnetwork:\n  management_interfaces: [eth0]\n"), 0600)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.HTTP.Port != 63200 || !c.Network.AllowFallback || c.DataDir != "/var/lib/nixmox" {
		t.Fatal(c)
	}
}

func TestFullRuntimeContract(t *testing.T) {
	body := []byte(`schema_version: 1
machine: {port: 63205}
nats: {client_port: 63201, route_port: 63202, leaf_port: 63203}
http: {port: 63200}
discovery: {enable: true, port: 63204}
network:
  management_interfaces: [eth0, tailscale0]
  preferred_interfaces: [tailscale0, eth0]
  allow_fallback: false
cluster: {name: homelab, join_address: "alder:63205"}
configuration: {repository: lab, name: alder}
repositories:
  lab: {remote_url: "https://example.test/nixos.git"}
  services: {remote_url: "ssh://git@example.test/services.git", upstream_branch: master, credential_ref: writer}
credentials:
  writer: {kind: git-ssh-key, secret_file: /run/credentials/missing-key}
capabilities: {build: true, install: true, media_write: true, power: true}
deployment: {keep_generations: 7}
cache: {enable: true, serve: false, signing_credential_ref: signer, max_concurrent_transfers: 9}
telemetry:
  host: {fast_interval_ms: 1000, filesystem_interval_ms: 5000}
  otlp: {enable: true, endpoint: "http://collector:4318", metrics_interval_ms: 2000}
prepare:
  sops: {update_rules: ["secrets.yaml"], updater_ref: null, credential_ref: age}
media:
  profiles:
    edge:
      label: Edge
      system: aarch64-linux
      format: raw
      compression: zstd
      supported_boards: [edge]
      boot_media: [sd]
      source: {repository: lab, ref: main, flake_attribute: images.edge, artifact_path: sd/edge.img.zst}
`)
	c := Default()
	if err := Decode(body, &c); err != nil {
		t.Fatal(err)
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if c.Repositories["lab"].UpstreamBranch != "main" || c.Repositories["services"].UpstreamBranch != "master" || c.NATS.ClientPort != 63201 || c.Network.AllowFallback || c.Media.Profiles["edge"].Source.ArtifactPath != "sd/edge.img.zst" {
		t.Fatalf("lost runtime settings: %#v", c)
	}
}
func TestRuntimeValidationGuards(t *testing.T) {
	cases := []string{
		"machine: {port: 63170}",
		"nats: {client_port: 0}",
		"cluster: {name: lab}",
		"cluster: {name: lab, join_address: 'alder'}",
		"credentials: {writer: {kind: git-http-token, secret_file: /nix/store/secret}}",
		"repositories: {lab: {remote_url: 'https://token@example.test/repo'}}",
		"repositories: {lab: {remote_url: 'ext::sh -c something'}}",
		"repositories: {lab: {remote_url: 'https://example.test/repo', upstream_branch: ''}}",
		"telemetry: {host: {fast_interval_ms: 200}}",
		"telemetry: {host: {fast_interval_ms: 4000, filesystem_interval_ms: 1000}}",
		"telemetry: {otlp: {enable: true}}",
		"cache: {max_concurrent_transfers: 0}",
		"deployment: {keep_generations: 0}",
	}
	for _, body := range cases {
		c := Default()
		err := Decode([]byte(body), &c)
		if err == nil {
			err = c.Validate()
		}
		if err == nil {
			t.Fatalf("accepted invalid runtime settings: %s", body)
		}
	}
	if err := ValidateRemote("ssh://git@[::1]:2222/repo"); err != nil {
		t.Fatal("IPv6 remote rejected", err)
	}
}
