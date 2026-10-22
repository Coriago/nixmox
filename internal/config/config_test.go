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
