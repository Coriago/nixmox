{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.services.nixmox;
  defaults = builtins.fromJSON (builtins.readFile ../internal/config/defaults.json);
  repositoryDefaults = builtins.fromJSON (
    builtins.readFile ../internal/config/repository-defaults.json
  );
  inherit (lib) mkOption types;
  opt =
    path: type:
    mkOption {
      inherit type;
      default = lib.attrByPath path null defaults;
    };
  required = type: mkOption { inherit type; };
  nullableString = types.nullOr types.str;
  snake =
    name:
    lib.concatMapStrings (c: if lib.toLower c != c then "_${lib.toLower c}" else c) (
      lib.stringToCharacters name
    );
  runtimeObject =
    value:
    if builtins.isAttrs value then
      lib.mapAttrs' (
        name: v:
        lib.nameValuePair (snake name) (
          if
            builtins.elem name [
              "repositories"
              "credentials"
              "profiles"
            ]
          then
            lib.mapAttrs (_: runtimeObject) v
          else
            runtimeObject v
        )
      ) value
    else if builtins.isList value then
      map runtimeObject value
    else
      value;
  runtimeValues = runtimeObject (
    builtins.removeAttrs cfg [
      "enable"
      "package"
      "openFirewall"
    ]
  );
  runtime = (pkgs.formats.yaml { }).generate "nixmox-config.yaml" (
    runtimeValues // { schema_version = defaults.schema_version; }
  );
  tcpPorts = [
    cfg.http.port
    cfg.machine.port
    cfg.nats.clientPort
    cfg.nats.routePort
    cfg.nats.leafPort
  ];
  managementPorts = [
    cfg.http.port
    cfg.machine.port
    cfg.nats.routePort
    cfg.nats.leafPort
  ];
  credentialKind = types.enum [
    "git-http-token"
    "git-ssh-key"
    "ssh-private-key"
    "nix-cache-signing-key"
    "sops-age-key"
  ];
in
{
  options.boot.kernel.sysctl = mkOption {
    type = types.submodule {
      options."net.ipv4.ip_local_reserved_ports" = mkOption {
        type = types.nullOr (types.separatedString ",");
        default = null;
        description = "Reserved ephemeral-port exclusions, merged across services.";
      };
    };
  };
  options.services.nixmox = {
    enable = lib.mkEnableOption "Nixmox node management";
    package = lib.mkPackageOption pkgs "nixmox" { };
    dataDir = opt [ "data_dir" ] types.str;
    http.port = opt [ "http" "port" ] types.port;
    machine.port = opt [ "machine" "port" ] types.port;
    nats = {
      clientPort = opt [ "nats" "client_port" ] types.port;
      routePort = opt [ "nats" "route_port" ] types.port;
      leafPort = opt [ "nats" "leaf_port" ] types.port;
    };
    network = {
      managementInterfaces = opt [ "network" "management_interfaces" ] (types.listOf types.str);
      preferredInterfaces = opt [ "network" "preferred_interfaces" ] (types.listOf types.str);
      allowFallback = opt [ "network" "allow_fallback" ] types.bool;
    };
    openFirewall = mkOption {
      type = types.bool;
      default = true;
    };
    discovery = {
      enable = opt [ "discovery" "enable" ] types.bool;
      port = opt [ "discovery" "port" ] types.port;
    };
    cluster = {
      name = opt [ "cluster" "name" ] nullableString;
      joinAddress = opt [ "cluster" "join_address" ] nullableString;
    };
    configuration = opt [ "configuration" ] (
      types.nullOr (
        types.submodule {
          options = {
            repository = required types.str;
            name = required types.str;
          };
        }
      )
    );
    repositories = mkOption {
      default = { };
      type = types.attrsOf (
        types.submodule {
          options = {
            remoteUrl = required types.str;
            upstreamBranch = mkOption {
              type = types.str;
              default = repositoryDefaults.upstream_branch;
            };
            credentialRef = mkOption {
              type = nullableString;
              default = repositoryDefaults.credential_ref;
            };
          };
        }
      );
    };
    credentials = mkOption {
      default = { };
      type = types.attrsOf (
        types.submodule {
          options = {
            kind = required credentialKind;
            secretFile = required types.str;
          };
        }
      );
    };
    capabilities = {
      build = opt [ "capabilities" "build" ] types.bool;
      install = opt [ "capabilities" "install" ] types.bool;
      mediaWrite = opt [ "capabilities" "media_write" ] types.bool;
      power = opt [ "capabilities" "power" ] types.bool;
    };
    deployment.keepGenerations = opt [ "deployment" "keep_generations" ] types.ints.positive;
    cache = {
      enable = opt [ "cache" "enable" ] types.bool;
      serve = opt [ "cache" "serve" ] types.bool;
      signingCredentialRef = opt [ "cache" "signing_credential_ref" ] nullableString;
      maxConcurrentTransfers = opt [ "cache" "max_concurrent_transfers" ] types.ints.positive;
    };
    telemetry = {
      host = {
        fastIntervalMs = opt [ "telemetry" "host" "fast_interval_ms" ] (types.ints.between 250 5000);
        filesystemIntervalMs = opt [ "telemetry" "host" "filesystem_interval_ms" ] (
          types.ints.between 1000 60000
        );
      };
      otlp = {
        enable = opt [ "telemetry" "otlp" "enable" ] types.bool;
        endpoint = opt [ "telemetry" "otlp" "endpoint" ] nullableString;
        metricsIntervalMs = opt [ "telemetry" "otlp" "metrics_interval_ms" ] (types.ints.between 250 60000);
      };
    };
    prepare.sops = {
      updateRules = opt [ "prepare" "sops" "update_rules" ] (types.listOf types.str);
      updaterRef = opt [ "prepare" "sops" "updater_ref" ] nullableString;
      credentialRef = opt [ "prepare" "sops" "credential_ref" ] nullableString;
    };
    media.profiles = mkOption {
      default = { };
      type = types.attrsOf (
        types.submodule {
          options = {
            label = required types.str;
            system = required types.str;
            format = required (
              types.enum [
                "iso"
                "raw"
              ]
            );
            compression = mkOption {
              type = types.enum [
                "none"
                "zstd"
                "xz"
                "gzip"
              ];
              default = "none";
            };
            supportedBoards = required (types.listOf types.str);
            bootMedia = required (types.listOf types.str);
            source = {
              repository = required types.str;
              ref = required types.str;
              flakeAttribute = required types.str;
              artifactPath = required types.str;
            };
          };
        }
      );
    };
  };
  config = lib.mkIf cfg.enable {
    assertions = [
      {
        assertion = lib.hasPrefix "/" cfg.dataDir;
        message = "services.nixmox.dataDir must be absolute";
      }
      {
        assertion = builtins.length (lib.unique tcpPorts) == builtins.length tcpPorts;
        message = "Nixmox TCP listener ports must be distinct";
      }
      {
        assertion = builtins.all (
          i: builtins.elem i cfg.network.managementInterfaces
        ) cfg.network.preferredInterfaces;
        message = "Nixmox preferred interfaces must be management interfaces";
      }
      {
        assertion = (cfg.cluster.name == null) == (cfg.cluster.joinAddress == null);
        message = "Nixmox cluster name and joinAddress must both be set";
      }
      {
        assertion = cfg.telemetry.host.filesystemIntervalMs >= cfg.telemetry.host.fastIntervalMs;
        message = "Nixmox filesystem interval must be at least the fast sampling interval";
      }
      {
        assertion = cfg.telemetry.otlp.metricsIntervalMs >= cfg.telemetry.host.fastIntervalMs;
        message = "Nixmox OTLP interval must be at least the fast sampling interval";
      }
      {
        assertion = !cfg.telemetry.otlp.enable || cfg.telemetry.otlp.endpoint != null;
        message = "Nixmox OTLP export requires an endpoint";
      }
      {
        assertion = builtins.all (c: !(lib.hasPrefix "/nix/store/" c.secretFile)) (
          builtins.attrValues cfg.credentials
        );
        message = "Nixmox credential files must remain outside the Nix store";
      }
    ];
    environment.systemPackages = [ cfg.package ];
    environment.etc."nixmox/config.yaml".source = runtime;
    environment.etc."nixmox/environment".text = lib.mkDefault "installed\n";
    nix.settings.extra-substituters = lib.mkIf cfg.cache.enable [
      "http://127.0.0.1:${toString cfg.http.port}/cache/?priority=10"
    ];
    nix.extraOptions = lib.mkAfter ''
      !include /run/nixmox-public/cache-trust.conf
    '';
    systemd.tmpfiles.settings."10-nixmox"."/run/nixmox-public".d = {
      mode = "0755";
      user = "root";
      group = "root";
    };
    systemd.tmpfiles.settings."10-nixmox".${cfg.dataDir}.d = {
      mode = "0700";
      user = "root";
      group = "root";
    };
    systemd.services.nixmox = {
      description = "Nixmox node management";
      wantedBy = [ "multi-user.target" ];
      wants = [ "network-online.target" ];
      after = [ "network-online.target" ];
      environment.NIXMOX_NIX_TRUST_FILE = "/run/nixmox-public/cache-trust.conf";
      path = with pkgs; [
        nix
        git
        openssh
        util-linux
        systemd
      ];
      serviceConfig = {
        ExecStart = "${lib.getExe cfg.package} serve --config /etc/nixmox/config.yaml";
        User = "root";
        Group = "root";
        Restart = "on-failure";
        RestartSec = 3;
        UMask = "0077";
        RuntimeDirectory = "nixmox";
        RuntimeDirectoryMode = "0700";
        NoNewPrivileges = true;
        PrivateTmp = true;
      };
    };
    networking.firewall.interfaces = lib.mkIf cfg.openFirewall (
      lib.genAttrs cfg.network.managementInterfaces (_: {
        allowedTCPPorts = managementPorts;
        allowedUDPPorts = lib.optional cfg.discovery.enable cfg.discovery.port;
      })
    );
    boot.kernel.sysctl."net.ipv4.ip_local_reserved_ports" = lib.concatMapStringsSep "," toString (
      tcpPorts ++ [ cfg.discovery.port ]
    );
  };
}
