package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"nixmox/internal/addhost"
	"nixmox/internal/config"
)

func TestAddHostCommand(t *testing.T) {
	old := cfg
	t.Cleanup(func() { cfg = old })
	dir := t.TempDir()
	template := filepath.Join(dir, "host.gotmpl")
	if err := os.WriteFile(template, []byte(`{ networking.hostName = {{ .Name }}; }`), 0600); err != nil {
		t.Fatal(err)
	}
	cfg = &config.Config{AddHost: &addhost.Config{Form: map[string]addhost.Field{"Name": {Type: "hostname", Default: "newhost"}}, Path: "hosts/{{ .Name }}.nix", Template: template}}
	run := func(args ...string) (string, error) {
		cmd := newAddHostCommand()
		cmd.SetArgs(args)
		cmd.SetIn(strings.NewReader(`{"cluster_address":"node1:8080"}`))
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		err := cmd.Execute()
		return out.String(), err
	}
	if _, err := run(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "hosts")); !os.IsNotExist(err) {
		t.Fatal("preview wrote files")
	}
	if _, err := run("--write", "--repo", dir); err != nil {
		t.Fatal(err)
	}
	if _, err := run("--write", "--repo", dir); err == nil {
		t.Fatal("overwrote host")
	}
}
