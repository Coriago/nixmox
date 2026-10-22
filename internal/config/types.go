package config

type Port struct {
	Port int `json:"port" yaml:"port"`
}
type Broker struct {
	ClientPort int `json:"client_port" yaml:"client_port"`
	RoutePort  int `json:"route_port" yaml:"route_port"`
	LeafPort   int `json:"leaf_port" yaml:"leaf_port"`
}
type Network struct {
	ManagementInterfaces []string `json:"management_interfaces" yaml:"management_interfaces"`
	PreferredInterfaces  []string `json:"preferred_interfaces" yaml:"preferred_interfaces"`
	AllowFallback        bool     `json:"allow_fallback" yaml:"allow_fallback"`
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
type Binding struct {
	Repository string `json:"repository" yaml:"repository"`
	Name       string `json:"name" yaml:"name"`
}
type Capabilities struct {
	Build      bool `json:"build" yaml:"build"`
	Install    bool `json:"install" yaml:"install"`
	MediaWrite bool `json:"media_write" yaml:"media_write"`
	Power      bool `json:"power" yaml:"power"`
}
type Cache struct {
	Enable                 bool    `json:"enable" yaml:"enable"`
	Serve                  bool    `json:"serve" yaml:"serve"`
	SigningCredentialRef   *string `json:"signing_credential_ref" yaml:"signing_credential_ref"`
	MaxConcurrentTransfers int     `json:"max_concurrent_transfers" yaml:"max_concurrent_transfers"`
}
type Telemetry struct {
	Host struct {
		FastIntervalMs       int `json:"fast_interval_ms" yaml:"fast_interval_ms"`
		FilesystemIntervalMs int `json:"filesystem_interval_ms" yaml:"filesystem_interval_ms"`
	} `json:"host" yaml:"host"`
	OTLP struct {
		Enable            bool    `json:"enable" yaml:"enable"`
		Endpoint          *string `json:"endpoint" yaml:"endpoint"`
		MetricsIntervalMs int     `json:"metrics_interval_ms" yaml:"metrics_interval_ms"`
	} `json:"otlp" yaml:"otlp"`
}
type Profile struct {
	Label           string   `json:"label" yaml:"label"`
	System          string   `json:"system" yaml:"system"`
	Format          string   `json:"format" yaml:"format"`
	Compression     string   `json:"compression" yaml:"compression"`
	SupportedBoards []string `json:"supported_boards" yaml:"supported_boards"`
	BootMedia       []string `json:"boot_media" yaml:"boot_media"`
	Source          struct {
		Repository     string `json:"repository" yaml:"repository"`
		Ref            string `json:"ref" yaml:"ref"`
		FlakeAttribute string `json:"flake_attribute" yaml:"flake_attribute"`
		ArtifactPath   string `json:"artifact_path" yaml:"artifact_path"`
	} `json:"source" yaml:"source"`
}
type Config struct {
	SchemaVersion int     `json:"schema_version" yaml:"schema_version"`
	DataDir       string  `json:"data_dir" yaml:"data_dir"`
	HTTP          Port    `json:"http" yaml:"http"`
	Machine       Port    `json:"machine" yaml:"machine"`
	NATS          Broker  `json:"nats" yaml:"nats"`
	Network       Network `json:"network" yaml:"network"`
	Discovery     struct {
		Enable bool `json:"enable" yaml:"enable"`
		Port   int  `json:"port" yaml:"port"`
	} `json:"discovery" yaml:"discovery"`
	Cluster struct {
		Name        *string `json:"name" yaml:"name"`
		JoinAddress *string `json:"join_address" yaml:"join_address"`
	} `json:"cluster" yaml:"cluster"`
	Configuration *Binding              `json:"configuration" yaml:"configuration"`
	Repositories  map[string]Repository `json:"repositories" yaml:"repositories"`
	Credentials   map[string]Credential `json:"credentials" yaml:"credentials"`
	Capabilities  Capabilities          `json:"capabilities" yaml:"capabilities"`
	Deployment    struct {
		KeepGenerations int `json:"keep_generations" yaml:"keep_generations"`
	} `json:"deployment" yaml:"deployment"`
	Cache     Cache     `json:"cache" yaml:"cache"`
	Telemetry Telemetry `json:"telemetry" yaml:"telemetry"`
	Prepare   struct {
		SOPS struct {
			UpdateRules   []string `json:"update_rules" yaml:"update_rules"`
			UpdaterRef    *string  `json:"updater_ref" yaml:"updater_ref"`
			CredentialRef *string  `json:"credential_ref" yaml:"credential_ref"`
		} `json:"sops" yaml:"sops"`
	} `json:"prepare" yaml:"prepare"`
	Media struct {
		Profiles map[string]Profile `json:"profiles" yaml:"profiles"`
	} `json:"media" yaml:"media"`
}
