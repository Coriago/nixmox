// Package runtime owns the canonical configuration defaults and parser.
package runtime

type Config struct {
	NodeName      string                `json:"node_name" yaml:"node_name"`
	SchemaVersion int                   `json:"schema_version" yaml:"schema_version"`
	DataDir       string                `json:"data_dir" yaml:"data_dir"`
	HTTP          HTTP                  `json:"http" yaml:"http"`
	NATS          NATS                  `json:"nats" yaml:"nats"`
	Network       Network               `json:"network" yaml:"network"`
	OpenFirewall  bool                  `json:"open_firewall" yaml:"open_firewall"`
	Discovery     Discovery             `json:"discovery" yaml:"discovery"`
	Cluster       Cluster               `json:"cluster" yaml:"cluster"`
	Configuration *Assignment           `json:"configuration" yaml:"configuration"`
	Repositories  map[string]Repository `json:"repositories" yaml:"repositories"`
	Credentials   map[string]Credential `json:"credentials" yaml:"credentials"`
	Capabilities  Capabilities          `json:"capabilities" yaml:"capabilities"`
	Deployment    Deployment            `json:"deployment" yaml:"deployment"`
	Cache         Cache                 `json:"cache" yaml:"cache"`
	Telemetry     Telemetry             `json:"telemetry" yaml:"telemetry"`
	Prepare       Prepare               `json:"prepare" yaml:"prepare"`
	Media         Media                 `json:"media" yaml:"media"`
}

type HTTP struct {
	Port int `json:"port" yaml:"port"`
}

type NATS struct {
	ClientPort int `json:"client_port" yaml:"client_port"`
	RoutePort  int `json:"route_port" yaml:"route_port"`
	LeafPort   int `json:"leaf_port" yaml:"leaf_port"`
}

type Network struct {
	ManagementInterfaces []string `json:"management_interfaces" yaml:"management_interfaces"`
	PreferredInterfaces  []string `json:"preferred_interfaces" yaml:"preferred_interfaces"`
	AllowFallback        bool     `json:"allow_fallback" yaml:"allow_fallback"`
}

type Discovery struct {
	Enable bool `json:"enable" yaml:"enable"`
	Port   int  `json:"port" yaml:"port"`
}

type Cluster struct {
	Name        *string `json:"name" yaml:"name"`
	JoinAddress *string `json:"join_address" yaml:"join_address"`
}

type Assignment struct {
	Repository string `json:"repository" yaml:"repository"`
	Name       string `json:"name" yaml:"name"`
}

type Repository struct {
	RemoteURL      string  `json:"remote_url" yaml:"remote_url"`
	UpstreamBranch string  `json:"upstream_branch" yaml:"upstream_branch"`
	CredentialRef  *string `json:"credential_ref" yaml:"credential_ref"`
}

type Credential struct {
	Kind       string `json:"kind" yaml:"kind"`
	SecretFile string `json:"secret_file" yaml:"secret_file"`
}

type Capabilities struct {
	Build      bool `json:"build" yaml:"build"`
	Install    bool `json:"install" yaml:"install"`
	MediaWrite bool `json:"media_write" yaml:"media_write"`
	Power      bool `json:"power" yaml:"power"`
}

type Deployment struct {
	KeepGenerations int `json:"keep_generations" yaml:"keep_generations"`
}

type Cache struct {
	Enable                 bool    `json:"enable" yaml:"enable"`
	Serve                  bool    `json:"serve" yaml:"serve"`
	SigningCredentialRef   *string `json:"signing_credential_ref" yaml:"signing_credential_ref"`
	MaxConcurrentTransfers int     `json:"max_concurrent_transfers" yaml:"max_concurrent_transfers"`
}

type Telemetry struct {
	Host HostTelemetry `json:"host" yaml:"host"`
	OTLP OTLP          `json:"otlp" yaml:"otlp"`
}

type HostTelemetry struct {
	FastIntervalMS       int `json:"fast_interval_ms" yaml:"fast_interval_ms"`
	FilesystemIntervalMS int `json:"filesystem_interval_ms" yaml:"filesystem_interval_ms"`
}

type OTLP struct {
	Enable            bool    `json:"enable" yaml:"enable"`
	Endpoint          *string `json:"endpoint" yaml:"endpoint"`
	MetricsIntervalMS int     `json:"metrics_interval_ms" yaml:"metrics_interval_ms"`
}

type Prepare struct {
	SOPS SOPS `json:"sops" yaml:"sops"`
}

type SOPS struct {
	UpdateRules   []string `json:"update_rules" yaml:"update_rules"`
	UpdaterRef    *string  `json:"updater_ref" yaml:"updater_ref"`
	CredentialRef *string  `json:"credential_ref" yaml:"credential_ref"`
}

type Media struct {
	Profiles map[string]Profile `json:"profiles" yaml:"profiles"`
}

type Profile struct {
	Label           string        `json:"label" yaml:"label"`
	System          string        `json:"system" yaml:"system"`
	Format          string        `json:"format" yaml:"format"`
	Compression     string        `json:"compression" yaml:"compression"`
	SupportedBoards []string      `json:"supported_boards" yaml:"supported_boards"`
	BootMedia       []string      `json:"boot_media" yaml:"boot_media"`
	Source          ProfileSource `json:"source" yaml:"source"`
}

type ProfileSource struct {
	Repository     string `json:"repository" yaml:"repository"`
	Ref            string `json:"ref" yaml:"ref"`
	FlakeAttribute string `json:"flake_attribute" yaml:"flake_attribute"`
	ArtifactPath   string `json:"artifact_path" yaml:"artifact_path"`
}
