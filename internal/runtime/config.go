package runtime

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"unicode"

	"github.com/go-viper/mapstructure/v2"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
	"go.yaml.in/yaml/v3"
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

// Load uses an isolated Viper instance: defaults < YAML < explicitly changed
// flags. Ambient environment variables cannot override daemon configuration.
func Load(input io.Reader, flags *pflag.FlagSet) (Config, error) {
	d := defaultValues()
	v := viper.New()
	raw, err := json.Marshal(d.Runtime)
	if err != nil {
		return Config{}, err
	}
	var defaults map[string]any
	if err = json.Unmarshal(raw, &defaults); err != nil {
		return Config{}, err
	}
	for key, value := range defaults {
		v.SetDefault(key, value)
	}
	if input != nil {
		var values map[string]any
		decoder := yaml.NewDecoder(input)
		if err = decoder.Decode(&values); err != nil {
			return Config{}, fmt.Errorf("configuration: %w", err)
		}
		if values == nil {
			return Config{}, fmt.Errorf("configuration must be an object")
		}
		if err = decoder.Decode(new(any)); err != io.EOF {
			return Config{}, fmt.Errorf("configuration must contain one YAML document")
		}
		// Viper keys are case-insensitive. Catalog aliases are domain data, not
		// setting keys: decode these maps to their declared types before merging.
		if err = catalog[Repository](values, "repositories"); err != nil {
			return Config{}, err
		}
		if err = catalog[Credential](values, "credentials"); err != nil {
			return Config{}, err
		}
		if media, ok := values["media"].(map[string]any); ok {
			if err = catalog[Profile](media, "profiles"); err != nil {
				return Config{}, err
			}
		}
		if err = v.MergeConfigMap(values); err != nil {
			return Config{}, err
		}
	}
	if flags != nil {
		for name, key := range configFlags {
			if flag := flags.Lookup(name); flag != nil {
				if err = v.BindPFlag(key, flag); err != nil {
					return Config{}, err
				}
			}
		}
	}
	var c Config
	err = v.UnmarshalExact(&c, func(dc *mapstructure.DecoderConfig) {
		dc.TagName, dc.WeaklyTypedInput = "json", false
		dc.DecodeHook = func(from, to reflect.Type, value any) (any, error) {
			if from.Kind() == reflect.Float64 && to.Kind() == reflect.Int {
				number := value.(float64)
				if math.Trunc(number) != number || math.IsInf(number, 0) || math.IsNaN(number) {
					return nil, fmt.Errorf("expected an integer")
				}
			}
			return value, nil
		}
	})
	if err != nil {
		return c, fmt.Errorf("configuration: %w", err)
	}
	for name, repo := range c.Repositories {
		if repo.UpstreamBranch == "" {
			repo.UpstreamBranch = d.Repository.UpstreamBranch
		}
		c.Repositories[name] = repo
	}
	return c, c.Validate()
}

func catalog[T any](values map[string]any, key string) error {
	value, ok := values[key]
	if !ok || value == nil {
		return nil
	}
	var entries map[string]T
	decoder, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{
		Result: &entries, TagName: "json", ErrorUnused: true,
	})
	if err != nil {
		return err
	}
	if err = decoder.Decode(value); err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}
	values[key] = entries
	return nil
}

const ConfigPath = "/etc/nixmox/config.yaml"

// LoadFile permits a missing conventional file, never a missing explicit path.
func LoadFile(path string, flags *pflag.FlagSet) (Config, error) {
	explicit := path != ""
	if !explicit {
		path = ConfigPath
	}
	file, err := os.Open(path)
	if os.IsNotExist(err) && !explicit {
		return Load(nil, flags)
	}
	if err != nil {
		return Config{}, fmt.Errorf("config %s: %w", path, err)
	}
	defer file.Close()
	return Load(file, flags)
}

var configFlags = map[string]string{
	"node-name": "node_name", "data-dir": "data_dir", "http-port": "http.port",
	"nats-client-port": "nats.client_port", "nats-route-port": "nats.route_port", "nats-leaf-port": "nats.leaf_port",
	"management-interfaces": "network.management_interfaces", "discovery": "discovery.enable",
}

// AddFlags declares daemon settings using the same defaults as the NixOS module.
func AddFlags(flags *pflag.FlagSet) {
	c := Defaults()
	flags.String("node-name", c.NodeName, "Friendly node name (default: OS hostname)")
	flags.String("data-dir", c.DataDir, "Persistent state directory")
	flags.Int("http-port", c.HTTP.Port, "Console HTTP port")
	flags.Int("nats-client-port", c.NATS.ClientPort, "Loopback NATS TLS port")
	flags.Int("nats-route-port", c.NATS.RoutePort, "NATS route TLS port")
	flags.Int("nats-leaf-port", c.NATS.LeafPort, "NATS leaf TLS port")
	flags.StringSlice("management-interfaces", c.Network.ManagementInterfaces, "Allowed management interfaces (empty means loopback only)")
	flags.Bool("discovery", c.Discovery.Enable, "Advertise on allowed management interfaces")
}

func (c Config) Validate() error {
	fail := func(key, message string) error { return fmt.Errorf("%s: %s", key, message) }
	if c.SchemaVersion != 1 {
		return fail("schema_version", "only version 1 is supported")
	}
	if len(c.NodeName) > 128 || strings.TrimSpace(c.NodeName) != c.NodeName || strings.ContainsFunc(c.NodeName, unicode.IsControl) {
		return fail("node_name", "must be at most 128 bytes without control characters or surrounding whitespace")
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
