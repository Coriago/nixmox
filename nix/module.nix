{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.services.nixmox;
  defaults = builtins.fromJSON (builtins.readFile ../internal/config/defaults.json);
  yaml = pkgs.formats.yaml { };
  runtime = yaml.generate "nixmox-config.yaml" {
    schema_version = defaults.schema_version;
    data_dir = cfg.dataDir;
    http.port = cfg.http.port;
    network = {
      management_interfaces = cfg.network.managementInterfaces;
      preferred_interfaces = cfg.network.preferredInterfaces;
      allow_fallback = cfg.network.allowFallback;
    };
  };
in
{
  options.boot.kernel.sysctl = lib.mkOption {
    type = lib.types.submodule {
      options."net.ipv4.ip_local_reserved_ports" = lib.mkOption {
        type = lib.types.nullOr (lib.types.separatedString ",");
        default = null;
        description = "Reserved ephemeral-port exclusions, merged across services.";
      };
    };
  };
  # This first slice intentionally exposes only implemented runtime options.
  options.services.nixmox = {
    enable = lib.mkEnableOption "Nixmox local node management";
    package = lib.mkPackageOption pkgs "nixmox" { };
    dataDir = lib.mkOption {
      type = lib.types.str;
      default = defaults.data_dir;
    };
    http.port = lib.mkOption {
      type = lib.types.port;
      default = defaults.http.port;
    };
    network = {
      managementInterfaces = lib.mkOption {
        type = lib.types.listOf lib.types.str;
        default = defaults.network.management_interfaces;
        description = "Interfaces allowed console access; empty means loopback only.";
      };
      preferredInterfaces = lib.mkOption {
        type = lib.types.listOf lib.types.str;
        default = defaults.network.preferred_interfaces;
      };
      allowFallback = lib.mkOption {
        type = lib.types.bool;
        default = defaults.network.allow_fallback;
      };
    };
    openFirewall = lib.mkOption {
      type = lib.types.bool;
      default = true;
    };
  };
  config = lib.mkIf cfg.enable {
    assertions = [
      {
        assertion = lib.hasPrefix "/" cfg.dataDir;
        message = "services.nixmox.dataDir must be absolute";
      }
      {
        assertion = builtins.all (
          i: builtins.elem i cfg.network.managementInterfaces
        ) cfg.network.preferredInterfaces;
        message = "Nixmox preferred interfaces must be management interfaces";
      }
    ];
    environment.systemPackages = [ cfg.package ];
    environment.etc."nixmox/config.yaml".source = runtime;
    users.users.nixmox = {
      isSystemUser = true;
      group = "nixmox";
    };
    users.groups.nixmox = { };
    systemd.tmpfiles.settings."10-nixmox".${cfg.dataDir}.d = {
      mode = "0700";
      user = "nixmox";
      group = "nixmox";
    };
    systemd.services.nixmox = {
      description = "Nixmox local node management";
      wantedBy = [ "multi-user.target" ];
      wants = [ "network-online.target" ];
      after = [ "network-online.target" ];
      serviceConfig = {
        ExecStart = "${lib.getExe cfg.package} serve --config /etc/nixmox/config.yaml";
        User = "nixmox";
        Group = "nixmox";
        Restart = "on-failure";
        RestartSec = 3;
        UMask = "0077";
        NoNewPrivileges = true;
        ProtectSystem = "strict";
        ProtectHome = true;
        PrivateTmp = true;
        ReadWritePaths = [ cfg.dataDir ];
      };
    };
    networking.firewall.interfaces = lib.mkIf cfg.openFirewall (
      lib.genAttrs cfg.network.managementInterfaces (_: {
        allowedTCPPorts = [ cfg.http.port ];
      })
    );
    boot.kernel.sysctl."net.ipv4.ip_local_reserved_ports" = toString cfg.http.port;
  };
}
