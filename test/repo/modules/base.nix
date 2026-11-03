{
  config,
  lib,
  pkgs,
  ...
}:
{
  networking.firewall.allowedUDPPorts = lib.optional (
    config.services.nixmox.config.discovery-port != 0
  ) config.services.nixmox.config.discovery-port;
  networking.firewall.allowedTCPPorts = [
    22
    8080
  ];
  services.openssh = {
    enable = true;
    settings = {
      PasswordAuthentication = false;
      KbdInteractiveAuthentication = false;
      PermitRootLogin = "prohibit-password";
    };
  };
  environment.systemPackages = [
    pkgs.curl
    pkgs.iproute2
  ];
  nix.settings.experimental-features = [
    "nix-command"
    "flakes"
  ];
}
