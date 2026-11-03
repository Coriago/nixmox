# Generic test-machine boot support. Installed recipe outputs do not import this.
{ lib, modulesPath, ... }:
{
  imports = [ (modulesPath + "/virtualisation/qemu-vm.nix") ];
  services.openssh.settings.AuthorizedKeysFile = "/run/e2e-authorized-keys/%u";
  systemd.services.e2e-key = {
    before = [ "sshd.service" ];
    requiredBy = [ "sshd.service" ];
    unitConfig.RequiresMountsFor = "/run/e2e-public";
    serviceConfig.Type = "oneshot";
    script = ''
      install -D -m 600 /run/e2e-public/id_ed25519.pub /run/e2e-authorized-keys/root
    '';
  };
  virtualisation = {
    memorySize = 4096;
    cores = 4;
    diskSize = 16384;
    graphics = false;
    useBootLoader = lib.mkDefault true;
    mountHostNixStore = false;
    useHostCerts = false;
    # Device wiring and host port forwards are supplied by the runner.
    sharedDirectories = lib.mkForce {
      keys = {
        source = "$NIXMOX_E2E_PUBLIC";
        target = "/run/e2e-public";
        writable = false;
      };
    };
  };
}
