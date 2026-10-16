{ config, lib, pkgs, ... }:

with lib;

let
  cfg = config.services.nixmox;
in
{
  options.nixmox-inputs = mkOption {
    type = types.attrsOf (types.oneOf [ types.str types.bool types.int types.float ]);
    default = {};
    description = "Per-host scalar inputs imported outside GitOps during impure evaluation.";
  };

  options.services.nixmox = {
    enableMedia = mkEnableOption "destructive local USB installer writing";
    installerFlake = mkOption {
      type = types.str;
      default = "github:Coriago/nixmox#installer-iso";
      description = "Trusted x86_64 nixmox hybrid ISO flake output. Pin a revision for reproducible media; custom images may include SSH public keys.";
    };
    enableCustomTasks = mkEnableOption "custom command tasks on the trusted management network";
    enablePower = mkEnableOption "host power controls (trusted management network only)";
    enableProvision = mkEnableOption "destructive nixos-anywhere provisioning";
    discovery = mkEnableOption "untrusted LAN discovery advertisements";
    joinSeed = mkOption {
      type = types.str;
      default = "";
      description = "Explicit seed host:port to join on first boot. Empty requires a manual join.";
    };
    inputsFile = mkOption {
      type = types.str;
      default = "${cfg.dataDir}/inputs.json";
      description = "Local JSON inputs; evaluate with --impure. Do not store secrets here.";
    };
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

    autoApply = mkOption {
      type = types.bool;
      default = false;
      description = "Automatically nixos-rebuild when the gitops repo advances. False = manual apply via portal.";
    };

    applyMode = mkOption {
      type = types.enum [ "switch" "boot" "test" "dry-activate" ];
      default = "switch";
      description = "nixos-rebuild mode used for apply jobs ('boot' is safer for VM tests).";
    };

    advertiseAddr = mkOption {
      type = types.str;
      default = "";
      description = "LAN address other nodes use to reach this one (NATS cluster routes).";
    };

    gitops = {
      flakeDirectory = mkOption {
        type = types.str;
        default = "";
        description = "Relative directory containing flake.nix inside the Git repository. Empty selects its root.";
      };
      flakeAttribute = mkOption {
        type = types.str;
        default = "";
        description = "nixosConfigurations attribute to apply. Empty selects the local hostname.";
      };
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
    nixmox-inputs = mkDefault (
      if builtins.pathExists cfg.inputsFile
      then builtins.fromJSON (builtins.readFile cfg.inputsFile)
      else {}
    );
    nix.settings.experimental-features = [ "nix-command" "flakes" ];
    systemd.services.nixmox = {
      description = "nixmox management daemon";
      wantedBy = [ "multi-user.target" ];
      wants = [ "network-online.target" ];
      after = [ "network-online.target" ];

      restartIfChanged = false;
      unitConfig.X-StopOnRemoval = false;

      path = [ pkgs.git pkgs.nixos-rebuild pkgs.systemd pkgs.openssh pkgs.nix pkgs.diffutils ]
        ++ optional cfg.enableMedia pkgs.util-linux
        ++ optional cfg.enableProvision pkgs.nixos-anywhere;

      environment = {
        NIXMOX_ENABLE_MEDIA = boolToString cfg.enableMedia;
        NIXMOX_INSTALLER_FLAKE = cfg.installerFlake;
        NIXMOX_DATA_DIR = cfg.dataDir;
        NIXMOX_INPUTS_FILE = cfg.inputsFile;
        NIXMOX_ENABLE_CUSTOM_TASKS = boolToString cfg.enableCustomTasks;
        NIXMOX_FLAKE_DIRECTORY = cfg.gitops.flakeDirectory;
        NIXMOX_FLAKE_ATTRIBUTE = cfg.gitops.flakeAttribute;
        NIXMOX_ENABLE_POWER = boolToString cfg.enablePower;
        NIXMOX_ENABLE_PROVISION = boolToString cfg.enableProvision;
        NIXMOX_DISCOVERY = boolToString cfg.discovery;
        NIXMOX_JOIN_SEED = cfg.joinSeed;
        NIXMOX_HTTP_ADDR = "0.0.0.0:${toString cfg.httpPort}";
        NIXMOX_GIT_BRANCH = cfg.gitops.branch;
        NIXMOX_GIT_POLL_INTERVAL_SECONDS = toString cfg.gitops.pollIntervalSeconds;
        NIXMOX_ALLOW_CONFIG_WRITE = boolToString cfg.allowConfigWrite;
        NIXMOX_AUTO_APPLY = boolToString cfg.autoApply;
        NIXMOX_APPLY_MODE = cfg.applyMode;
      } // optionalAttrs (cfg.gitops.repoUrl != "") {
        NIXMOX_GIT_REPO_URL = cfg.gitops.repoUrl;
      } // optionalAttrs (cfg.gitops.sshKeyPath != "") {
        NIXMOX_GIT_SSH_KEY_PATH = cfg.gitops.sshKeyPath;
      } // optionalAttrs (cfg.gitops.netrcPath != "") {
        NIXMOX_GIT_NETRC_PATH = cfg.gitops.netrcPath;
      } // optionalAttrs (cfg.advertiseAddr != "") {
        NIXMOX_ADVERTISE_ADDR = cfg.advertiseAddr;
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

    networking.firewall.allowedUDPPorts = mkIf (cfg.openFirewall && cfg.discovery) [ 8099 ];
    networking.firewall.allowedTCPPorts = mkIf cfg.openFirewall [
      cfg.httpPort
      6222
    ];
  };
}
