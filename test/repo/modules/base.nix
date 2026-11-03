{ lib, modulesPath, pkgs, ... }:
{
  imports = [ (modulesPath + "/virtualisation/qemu-vm.nix") ];
  networking.hostName = "vm1";
  networking.firewall.allowedTCPPorts = [ 22 8080 ];
  services.openssh = {
    enable = true;
    settings = {
      PasswordAuthentication = false;
      KbdInteractiveAuthentication = false;
      PermitRootLogin = "prohibit-password";
      AuthorizedKeysFile = "/run/e2e-authorized-keys/%u";
    };
  };
  # Copy the public-only share into root-owned files for OpenSSH StrictModes.
  systemd.services.e2e-key = {
    before = [ "sshd.service" ];
    requiredBy = [ "sshd.service" ];
    unitConfig.RequiresMountsFor = "/run/e2e-public";
    serviceConfig.Type = "oneshot";
    script = ''
      install -D -m 600 /run/e2e-public/id_ed25519.pub /run/e2e-authorized-keys/root
    '';
  };
  environment.systemPackages = [ pkgs.curl pkgs.iproute2 ];
  nix.settings.experimental-features = [ "nix-command" "flakes" ];
  virtualisation = {
    memorySize = 4096;
    cores = 4;
    diskSize = 16384;
    graphics = false;
    useBootLoader = true;
    mountHostNixStore = false;
    useHostCerts = false;
    sharedDirectories = lib.mkForce {
      keys = {
        source = "$NIXMOX_E2E_PUBLIC";
        target = "/run/e2e-public";
        writable = false;
      };
    };
    forwardPorts = [
      { from = "host"; host.address = "127.0.0.1"; host.port = 22221; guest.port = 22; }
      { from = "host"; host.address = "127.0.0.1"; host.port = 18081; guest.port = 8080; }
    ];
  };
  system.stateVersion = "26.05";
}
