package config

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"go.yaml.in/yaml/v3"
)

//go:embed defaults.json
var defaults []byte

type Config struct {
	SchemaVersion int    `json:"schema_version" yaml:"schema_version"`
	DataDir       string `json:"data_dir" yaml:"data_dir"`
	HTTP          struct {
		Port int `json:"port" yaml:"port"`
	} `json:"http" yaml:"http"`
	Network struct {
		ManagementInterfaces []string `json:"management_interfaces" yaml:"management_interfaces"`
		PreferredInterfaces  []string `json:"preferred_interfaces" yaml:"preferred_interfaces"`
		AllowFallback        bool     `json:"allow_fallback" yaml:"allow_fallback"`
	} `json:"network" yaml:"network"`
}

func Default() Config {
	var c Config
	if err := json.Unmarshal(defaults, &c); err != nil {
		panic(err)
	}
	return c
}
func Load(path string) (Config, error) {
	c := Default()
	if path != "" {
		f, err := os.Open(path)
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
		d := yaml.NewDecoder(bytes.NewReader(b))
		d.KnownFields(true)
		if err := d.Decode(&c); err != nil {
			return c, errors.New("invalid configuration: unknown field or invalid value")
		}
		var extra any
		if err := d.Decode(&extra); err != io.EOF {
			return c, errors.New("configuration must contain one document")
		}
	}
	return c, c.Validate()
}
func (c Config) Validate() error {
	if c.SchemaVersion != 1 {
		return errors.New("unsupported schema_version")
	}
	if !filepath.IsAbs(c.DataDir) {
		return errors.New("data_dir must be absolute")
	}
	if c.HTTP.Port < 1 || c.HTTP.Port > 65535 {
		return errors.New("http.port must be between 1 and 65535")
	}
	seen := map[string]bool{}
	for _, n := range c.Network.ManagementInterfaces {
		if n == "" || len(n) > 15 || seen[n] {
			return errors.New("invalid or duplicate management interface")
		}
		seen[n] = true
	}
	for _, n := range c.Network.PreferredInterfaces {
		if !seen[n] {
			return fmt.Errorf("preferred interface %q is not a management interface", n)
		}
	}
	return nil
}
