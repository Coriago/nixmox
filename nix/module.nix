self:
{
  config,
  lib,
  pkgs,
  ...
}:
let
  inherit (lib)
    mkOption
    mkIf
    mkDefault
    types
    ;
  cfg = config.services.nixmox;
  defaults = builtins.fromJSON (builtins.readFile ../internal/runtime/defaults.json);
  d = defaults.runtime;
  opt =
    type: default: description:
    mkOption { inherit type default description; };
  nullable = types.nullOr types.str;
  strings = types.listOf types.str;
  positive = types.ints.positive;
  port = types.port;
  required = type: description: mkOption { inherit type description; };
  sub = options: types.submodule { inherit options; };
  snake =
    name:
    lib.concatMapStrings (c: if builtins.match "[A-Z]" c != null then "_${lib.toLower c}" else c) (
      lib.stringToCharacters name
    );
  # Catalog aliases are opaque labels: do not rewrite repository/credential/profile keys.
  encode =
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
            lib.mapAttrs (_: encode) v
          else
            encode v
        )
      ) value
    else if builtins.isList value then
      map encode value
    else
      value;
  runtime =
    encode (
      builtins.removeAttrs cfg [
        "enable"
        "package"
      ]
    )
    // {
      schema_version = d.schema_version;
    };
  configFile = pkgs.writeText "nixmox-config.yaml" (builtins.toJSON runtime);
  tcpPorts = [
    cfg.http.port
    cfg.nats.clientPort
    cfg.nats.routePort
    cfg.nats.leafPort
  ];
  publicPorts = [
    cfg.http.port
    cfg.nats.routePort
    cfg.nats.leafPort
  ];
in
{
  options.services.nixmox = {
    enable = lib.mkEnableOption "Nixmox (rewrite scaffold; not operational yet)";
    package =
      opt types.package self.packages.${pkgs.stdenv.hostPlatform.system}.default
        "Nixmox package.";
    dataDir = opt types.str d.data_dir "Persistent runtime state directory.";
    http.port = opt port d.http.port "Console, enrollment and cache HTTP port.";
    nats = {
      clientPort = opt port d.nats.client_port "Loopback-only TLS client port.";
      routePort = opt port d.nats.route_port "Full-broker TLS route port.";
      leafPort = opt port d.nats.leaf_port "Scoped pending-broker TLS leaf port.";
    };
    network = {
      managementInterfaces =
        opt strings d.network.management_interfaces
          "Interfaces permitted to access management services; empty means loopback-only.";
      preferredInterfaces =
        opt strings d.network.preferred_interfaces
          "Ordered allowed agent endpoint preferences.";
      allowFallback = opt types.bool d.network.allow_fallback "Permit less-preferred allowed endpoints.";
    };
    openFirewall = opt types.bool d.open_firewall "Open service ports only on management interfaces.";
    discovery = {
      enable = opt types.bool d.discovery.enable "Advertise on allowed LAN interfaces.";
      port = opt port d.discovery.port "Shared UDP discovery port.";
    };
    cluster = {
      name = opt nullable d.cluster.name "Expected cluster name; set with joinAddress.";
      joinAddress = opt nullable d.cluster.join_address "Peer HTTP host:port awaiting approval.";
    };
    configuration = opt (types.nullOr (sub {
      repository = required types.str "Repository alias.";
      name = required types.str "Configuration name; intent is not deployment evidence.";
    })) d.configuration "Intended configuration association.";
    repositories = opt (types.attrsOf (sub {
      remoteUrl = required types.str "Remote Git base URL.";
      upstreamBranch = opt types.str defaults.repository.upstream_branch "Official upstream branch.";
      credentialRef = opt nullable defaults.repository.credential_ref "Optional credential alias.";
    })) d.repositories "Declared remote-backed repositories; never implicitly pushed.";
    credentials = opt (types.attrsOf (sub {
      kind = required (types.enum [
        "git-http-token"
        "git-ssh-key"
        "ssh-private-key"
        "nix-cache-signing-key"
        "sops-age-key"
      ]) "Credential kind.";
      secretFile = required types.str "Runtime secret path, never a Nix path literal.";
    })) d.credentials "Local credential sources; contents never enter the store.";
    capabilities = {
      build = opt types.bool d.capabilities.build "Permit builds.";
      install = opt types.bool d.capabilities.install "Permit reviewed installation effects.";
      mediaWrite = opt types.bool d.capabilities.media_write "Permit reviewed local media writes.";
      power = opt types.bool d.capabilities.power "Permit reviewed power transitions.";
    };
    deployment.keepGenerations =
      opt positive d.deployment.keep_generations
        "Recent outputs retained in addition to active pins.";
    cache = {
      enable = opt types.bool d.cache.enable "Enable the local streaming cache gateway.";
      serve =
        opt types.bool d.cache.serve
          "Permit store serving when membership and signing key are ready.";
      signingCredentialRef =
        opt nullable d.cache.signing_credential_ref
          "Local signing credential alias.";
      maxConcurrentTransfers =
        opt positive d.cache.max_concurrent_transfers
          "Independent incoming/outgoing stream bounds.";
    };
    telemetry = {
      host = {
        fastIntervalMs =
          opt (types.ints.between 250 5000) d.telemetry.host.fast_interval_ms
            "Fast OS sampling interval.";
        filesystemIntervalMs =
          opt (types.ints.between 1000 60000) d.telemetry.host.filesystem_interval_ms
            "Independent filesystem sampling interval.";
      };
      otlp = {
        enable = opt types.bool d.telemetry.otlp.enable "Optional bounded OTLP export.";
        endpoint = opt nullable d.telemetry.otlp.endpoint "OTLP HTTP endpoint; required when enabled.";
        metricsIntervalMs =
          opt (types.ints.between 250 60000) d.telemetry.otlp.metrics_interval_ms
            "Export interval, independent of sampling.";
      };
    };
    prepare.sops = {
      updateRules =
        opt strings d.prepare.sops.update_rules
          "Templated exact selectors for existing SOPS rules.";
      updaterRef =
        opt nullable d.prepare.sops.updater_ref
          "Custom updater reference; null selects bundled anchors.";
      credentialRef =
        opt nullable d.prepare.sops.credential_ref
          "Age credential for recipient updates; null skips.";
    };
    media.profiles = opt (types.attrsOf (sub {
      label = required types.str "Profile label.";
      system = required types.str "Target Nix system.";
      format = required (types.enum [
        "iso"
        "raw"
      ]) "Image format.";
      compression = required (types.enum [
        "none"
        "zstd"
        "gzip"
        "xz"
      ]) "Image compression.";
      supportedBoards = required strings "Explicit board compatibility.";
      bootMedia = required strings "Declared supported media.";
      source = {
        repository = required types.str "Repository alias.";
        ref = required types.str "Recipe ref, pinned before execution.";
        flakeAttribute = required types.str "Exported image derivation.";
        artifactPath = required types.str "Exact output-relative image path.";
      };
    })) d.media.profiles "Additional Git-backed installer recipes.";
  };

  # Extend the sysctl value's merge type so reservations from other modules survive.
  options.boot.kernel.sysctl = mkOption {
    type = types.submodule {
      options."net.ipv4.ip_local_reserved_ports" = mkOption {
        type = types.nullOr (types.separatedString ",");
        default = null;
        description = "Merged TCP/UDP port reservations.";
      };
    };
  };

  config = mkIf cfg.enable {
    assertions = [
      {
        assertion = builtins.length (lib.unique tcpPorts) == 4;
        message = "Nixmox TCP listener ports must not overlap.";
      }
      {
        assertion = (cfg.cluster.name == null) == (cfg.cluster.joinAddress == null);
        message = "Nixmox cluster name and joinAddress must be set together.";
      }
      {
        assertion = !cfg.telemetry.otlp.enable || cfg.telemetry.otlp.endpoint != null;
        message = "Nixmox OTLP requires an endpoint.";
      }
      {
        assertion = cfg.telemetry.host.filesystemIntervalMs >= cfg.telemetry.host.fastIntervalMs;
        message = "Nixmox filesystem interval must be at least the fast interval.";
      }
      {
        assertion = cfg.telemetry.otlp.metricsIntervalMs >= cfg.telemetry.host.fastIntervalMs;
        message = "Nixmox export interval must be at least the fast interval.";
      }
      {
        assertion = lib.all (
          i: builtins.elem i cfg.network.managementInterfaces
        ) cfg.network.preferredInterfaces;
        message = "Nixmox preferred interfaces must be allowed management interfaces.";
      }
      {
        assertion = lib.all (
          c: lib.hasPrefix "/" c.secretFile && !(lib.hasPrefix builtins.storeDir c.secretFile)
        ) (builtins.attrValues cfg.credentials);
        message = "Nixmox secrets require absolute runtime paths outside the Nix store.";
      }
    ];
    environment.systemPackages = [ cfg.package ];
    environment.etc."nixmox/config.yaml".source = configFile;
    boot.kernel.sysctl."net.ipv4.ip_local_reserved_ports" = lib.mkAfter (
      lib.concatMapStringsSep "," toString (tcpPorts ++ [ cfg.discovery.port ])
    );
    networking.firewall.interfaces = mkIf cfg.openFirewall (
      lib.genAttrs cfg.network.managementInterfaces (_: {
        allowedTCPPorts = publicPorts;
        allowedUDPPorts = lib.optional cfg.discovery.enable cfg.discovery.port;
      })
    );
    systemd.tmpfiles.rules = [ "d '${cfg.dataDir}' 0700 root root - -" ];
    systemd.services.nixmox = {
      description = "Nixmox cluster lifecycle manager";
      wantedBy = [ "multi-user.target" ];
      wants = [ "network-online.target" ];
      after = [ "network-online.target" ];
      serviceConfig = {
        ExecStart = "${cfg.package}/bin/nixmox serve --config /etc/nixmox/config.yaml";
        Restart = "on-failure";
        RestartSec = 5;
        UMask = "0077";
        # Installation/device effects require root; per-phase capabilities come later.
        User = "root";
      };
    };
    # Trusted peer keys are added only from authenticated membership metadata.
    nix.settings = mkIf cfg.cache.enable {
      extra-substituters = [ "http://127.0.0.1:${toString cfg.http.port}/cache/" ];
      fallback = mkDefault true;
    };
  };
}
