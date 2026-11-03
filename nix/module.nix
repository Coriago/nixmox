self:
{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.services.nixmox;
  format = pkgs.formats.json { };
  configFile = format.generate "nixmox.json" cfg.config;
in
{
  options.services.nixmox = {
    enable = lib.mkEnableOption "the Nixmox host-management daemon";
    package = lib.mkOption {
      type = lib.types.package;
      default = self.packages.${pkgs.stdenv.hostPlatform.system}.default;
      defaultText = lib.literalExpression "nixmox.packages.\${pkgs.stdenv.hostPlatform.system}.default";
      description = "Nixmox package providing bin/nixmox.";
    };
    config = lib.mkOption {
      default = { };
      description = ''
        Daemon configuration, serialized as JSON and passed with --config.
        Keys match daemon flags. Do not put secrets here: this file is public
        in the Nix store. Use runtime file paths for private keys.
        Firewall rules and SSH host-key provisioning remain host-owned.
      '';
      type = lib.types.submodule {
        options = {
          environment = lib.mkOption {
            type = lib.types.enum [
              "installed"
              "installer"
            ];
            default = "installed";
            description = "Current boot environment, independent of membership status.";
          };
          name = lib.mkOption {
            type = lib.types.str;
            default = "";
            description = "Node name; empty uses the hostname on first startup.";
          };
          state-dir = lib.mkOption {
            type = lib.types.strMatching "/.+";
            default = "/var/lib/nixmox";
            description = "Absolute path to private persistent daemon state.";
          };
          listen = lib.mkOption {
            type = lib.types.str;
            default = "127.0.0.1:8080";
            description = "HTTP API listen address. Remote access must be protected by the host.";
          };
          advertise = lib.mkOption {
            type = lib.types.str;
            default = "http://127.0.0.1:8080";
            description = "HTTP URL advertised to peers; configure alongside listen.";
          };
          nats-host = lib.mkOption {
            type = lib.types.str;
            default = "127.0.0.1";
            description = "Bind address for embedded NATS client and route listeners.";
          };
          nats-port = lib.mkOption {
            type = lib.types.port;
            default = 4222;
            description = "Main NATS client port.";
          };
          route-port = lib.mkOption {
            type = lib.types.port;
            default = 6222;
            description = "Main NATS route port.";
          };
          bootstrap-port = lib.mkOption {
            type = lib.types.port;
            default = 4223;
            description = "Bootstrap NATS client port.";
          };
          bootstrap-route-port = lib.mkOption {
            type = lib.types.port;
            default = 6223;
            description = "Bootstrap NATS route port.";
          };
          ssh-host-key = lib.mkOption {
            type = lib.types.strMatching "/.+";
            default = "/etc/ssh/ssh_host_ed25519_key";
            description = "Existing runtime SSH private host-key path used to derive the age identity.";
          };
        };
      };
    };
  };

  config = lib.mkIf cfg.enable {
    assertions = [
      {
        assertion =
          let
            ports = with cfg.config; [
              nats-port
              route-port
              bootstrap-port
              bootstrap-route-port
            ];
          in
          builtins.length (lib.unique ports) == 4;
        message = "services.nixmox.config NATS client and route ports must be distinct.";
      }
    ];
    environment.systemPackages = [ cfg.package ];
    systemd.services.nixmox = {
      description = "Nixmox host-management daemon";
      wantedBy = [ "multi-user.target" ];
      wants = [ "network-online.target" ];
      after = [
        "network-online.target"
        "sshd-keygen.service"
      ];
      path = [
        pkgs.nix
        pkgs.sops
        pkgs.ssh-to-age
        pkgs.git
        pkgs.openssh
      ];
      serviceConfig = {
        Type = "simple";
        ExecStart = "${lib.getExe' cfg.package "nixmox"} daemon --config ${configFile}";
        WorkingDirectory = cfg.config.state-dir;
        # Host operations and reading the private SSH host key require root.
        User = "root";
        UMask = "0077";
        Restart = "on-failure";
        RestartSec = 5;
        TimeoutStopSec = 30;
      }
      // lib.optionalAttrs (cfg.config.state-dir == "/var/lib/nixmox") {
        StateDirectory = "nixmox";
        StateDirectoryMode = "0700";
      };
    };
    systemd.tmpfiles.settings.nixmox = lib.mkIf (cfg.config.state-dir != "/var/lib/nixmox") {
      ${cfg.config.state-dir}.d = {
        mode = "0700";
        user = "root";
        group = "root";
      };
    };
  };
}
