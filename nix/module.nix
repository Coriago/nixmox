{ config, lib, pkgs, ... }:

with lib;

let
  cfg = config.services.nixmox;
in
{
  options.services.nixmox = {
    enable = mkEnableOption "nixmox management daemon";

    package = mkOption {
      type = types.package;
      description = "The nixmox package to use.";
    };

    dataDir = mkOption {
      type = types.str;
      default = "/var/lib/nixmox";
      description = "Directory where nixmox stores persistent data.";
    };

    httpPort = mkOption {
      type = types.port;
      default = 8080;
      description = "HTTP API and portal listen port.";
    };

    openFirewall = mkOption {
      type = types.bool;
      default = true;
      description = "Open firewall ports for nixmox (HTTP and cluster).";
    };

    allowConfigWrite = mkOption {
      type = types.bool;
      default = false;
      description = "Allow runtime config changes via the portal/API (e.g. setting the gitops repo URL).";
    };

    gitops = {
      repoUrl = mkOption {
        type = types.str;
        default = "";
        description = "Git URL of the NixOS config repo. Empty = unconfigured (set later via portal).";
      };
      branch = mkOption {
        type = types.str;
        default = "main";
        description = "Branch to track.";
      };
      pollIntervalSeconds = mkOption {
        type = types.int;
        default = 60;
        description = "Interval between git fetch/update checks. 0 disables polling.";
      };
      sshKeyPath = mkOption {
        type = types.str;
        default = "";
        description = "SSH private key path for authenticated git remotes.";
      };
      netrcPath = mkOption {
        type = types.str;
        default = "";
        description = "netrc file path for HTTPS git remotes.";
      };
    };
  };

  config = mkIf cfg.enable {
    systemd.services.nixmox = {
      description = "nixmox management daemon";
      wantedBy = [ "multi-user.target" ];
      after = [ "network.target" ];

      restartIfChanged = false;
      unitConfig.X-StopOnRemoval = false;

      path = [ pkgs.git ];

      environment = {
        NIXMOX_DATA_DIR = cfg.dataDir;
        NIXMOX_HTTP_ADDR = "0.0.0.0:${toString cfg.httpPort}";
        NIXMOX_GIT_BRANCH = cfg.gitops.branch;
        NIXMOX_GIT_POLL_INTERVAL_SECONDS = toString cfg.gitops.pollIntervalSeconds;
        NIXMOX_ALLOW_CONFIG_WRITE = boolToString cfg.allowConfigWrite;
      } // optionalAttrs (cfg.gitops.repoUrl != "") {
        NIXMOX_GIT_REPO_URL = cfg.gitops.repoUrl;
      } // optionalAttrs (cfg.gitops.sshKeyPath != "") {
        NIXMOX_GIT_SSH_KEY_PATH = cfg.gitops.sshKeyPath;
      } // optionalAttrs (cfg.gitops.netrcPath != "") {
        NIXMOX_GIT_NETRC_PATH = cfg.gitops.netrcPath;
      };

      serviceConfig = {
        Type = "simple";
        ExecStart = "${cfg.package}/bin/nixmox serve";
        KillMode = "process";
        Restart = "on-failure";
        RestartSec = 5;
        User = "root";
        Group = "root";
        StateDirectory = "nixmox";
        StateDirectoryMode = "0750";
        WorkingDirectory = cfg.dataDir;
      };
    };

    networking.firewall.allowedTCPPorts = mkIf cfg.openFirewall [
      cfg.httpPort
      6222
    ];
  };
}
