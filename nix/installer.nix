# Shared live-media profile. Add SSH public keys when building your own image.
{ package, sshKeys ? [] }:
{ lib, pkgs, ... }: {
  imports = [ ./module.nix ./installer-wifi.nix ];
  networking.hostName = lib.mkForce "nixmox-live";
  services.nixmox = {
    enable = true;
    installerMode = true;
    inherit package;
    discovery = true;
    allowConfigWrite = true;
    enablePower = true;
  };
  services.openssh = {
    enable = true;
    settings = {
      PasswordAuthentication = false;
      KbdInteractiveAuthentication = false;
      PermitRootLogin = "prohibit-password";
    };
  };
  users.users.root.openssh.authorizedKeys.keys = sshKeys;
  systemd.services.nixmox-identity = {
    wantedBy = [ "multi-user.target" ];
    before = [ "nixmox.service" ];
    after = [ "systemd-machine-id-commit.service" ];
    serviceConfig.Type = "oneshot";
    script = ''
      ${pkgs.systemd}/bin/hostnamectl set-hostname "nixmox-$(${pkgs.coreutils}/bin/head -c 8 /etc/machine-id)"
    '';
  };
  environment.systemPackages = [ pkgs.git pkgs.jq ];
  system.stateVersion = "24.11";
}
