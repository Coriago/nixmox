package addhost

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixture(t *testing.T) (Config, Input) {
	t.Helper()
	file := filepath.Join(t.TempDir(), "host.gotmpl")
	if err := os.WriteFile(file, []byte(`{ meta.{{ .Name }} = {{ .Text }}; imports = [ ({{ .Hardware }}) ]; modules = [{{ range .Modules }} {{ . }}{{ end }} ]; drive = {{ .Drive }}; seed = {{ .ClusterAddress }}; }`), 0600); err != nil {
		t.Fatal(err)
	}
	return Config{Template: file, Path: "hosts/{{ .Name }}/configuration.nix", Form: map[string]Field{
		"Name": {Type: "hostname", Default: "newhost"}, "Text": {Type: "text"},
		"Modules": {Type: "multiselect", Values: []string{"base"}, Default: []string{"base"}},
		"Drive":   {Type: "disk", Required: true},
	}}, Input{Values: map[string]any{"Drive": "/dev/vda", "Text": "${danger}\"\\\n"}, Hardware: "{ config, ... }: { imports = []; }", ClusterAddress: "node1:8080", Disks: []Disk{{Path: "/dev/vda", Whole: true}}}
}

func TestGenerate(t *testing.T) {
	c, in := fixture(t)
	r, err := Generate(c, in)
	if err != nil {
		t.Fatal(err)
	}
	if r.Path != "hosts/newhost/configuration.nix" || !strings.Contains(r.Content, `meta."newhost"`) || !strings.Contains(r.Content, `"\${danger}\"\\\n"`) || !strings.Contains(r.Content, "("+in.Hardware+")") {
		t.Fatalf("unexpected result: %+v", r)
	}
	repo := t.TempDir()
	if err := Write(repo, r); err != nil {
		t.Fatal(err)
	}
	if err := Write(repo, r); err == nil {
		t.Fatal("overwrote configuration")
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(repo, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := Write(repo, Result{Path: "escape/host.nix", Content: "bad"}); err == nil {
		t.Fatal("escaped repository")
	}
}

func TestInvalidInput(t *testing.T) {
	cases := map[string]func(*Config, *Input){
		"hostname":         func(c *Config, i *Input) { i.Values["Name"] = "../bad" },
		"username":         func(c *Config, i *Input) { c.Form["User"] = Field{Type: "username", Default: "root;bad"} },
		"unknown":          func(c *Config, i *Input) { i.Values["Hardware"] = "evil" },
		"module":           func(c *Config, i *Input) { i.Values["Modules"] = []string{"unknown"} },
		"duplicate":        func(c *Config, i *Input) { i.Values["Modules"] = []string{"base", "base"} },
		"installer":        func(c *Config, i *Input) { i.Disks[0].Installer = true },
		"partition":        func(c *Config, i *Input) { i.Disks[0].Whole = false },
		"missing disk":     func(c *Config, i *Input) { delete(i.Values, "Drive") },
		"traversal":        func(c *Config, i *Input) { c.Path = "../host.nix" },
		"absolute":         func(c *Config, i *Input) { c.Path = "/tmp/host.nix" },
		"missing key":      func(c *Config, i *Input) { c.Path = "{{ .Missing }}" },
		"missing hardware": func(c *Config, i *Input) { i.Hardware = "" },
		"seed":             func(c *Config, i *Input) { i.ClusterAddress = "node1:bad" },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			c, i := fixture(t)
			change(&c, &i)
			if _, err := Generate(c, i); err == nil {
				t.Fatal("accepted invalid input")
			}
		})
	}
}

func TestHardwareOptional(t *testing.T) {
	c, i := fixture(t)
	i.Hardware = ""
	if err := os.WriteFile(c.Template, []byte(`{ name = {{ .Name }}; }`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Generate(c, i); err != nil {
		t.Fatal(err)
	}
}
