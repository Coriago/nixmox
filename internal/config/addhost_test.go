package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAddHostConfigFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "add-host.json")
	if err := os.WriteFile(file, []byte(`{"form":{"Name":{"type":"hostname","default":"newhost"}},"path":"hosts/{{ .Name }}.nix","template":"/example/host.gotmpl"}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NIXMOX_ADD_HOST_CONFIG", file)
	c, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if c.AddHost == nil || c.AddHost.Form["Name"].Default != "newhost" {
		t.Fatalf("missing generator settings: %+v", c.AddHost)
	}
	t.Setenv("NIXMOX_ADD_HOST_CONFIG", file+"-missing")
	if _, err := Load(""); err == nil {
		t.Fatal("ignored missing generator config")
	}
}
