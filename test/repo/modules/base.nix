{ lib, modulesPath, pkgs, vmIndex ? 1, ... }:
let
  mac = "52:54:00:77:00:0${toString vmIndex}";
in
{
  imports = [ (modulesPath + "/virtualisation/qemu-vm.nix") ];
  networking.hostName = "vm${toString vmIndex}";
  networking.interfaces.cluster0 = {
    useDHCP = false;
    ipv4.addresses = [ { address = "192.168.77.${toString vmIndex}"; prefixLength = 24; } ];
  };
  networking.firewall.trustedInterfaces = [ "cluster0" ];
  systemd.network.links."10-e2e-cluster" = {
    matchConfig.MACAddress = mac;
    linkConfig.Name = "cluster0";
  };
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
    # QEMU's userspace hub provides a shared guest LAN without host tap/bridges.
    # VM1 owns two localhost-only socket links, ready for VM2 and VM3.
    qemu.options = [
      "-device virtio-net-pci,netdev=cluster,mac=${mac}"
    ] ++ (if vmIndex == 1 then [
      "-netdev hubport,id=cluster,hubid=0"
      "-netdev socket,id=peer2,listen=127.0.0.1:23222"
      "-netdev hubport,id=port2,hubid=0,netdev=peer2"
      "-netdev socket,id=peer3,listen=127.0.0.1:23223"
      "-netdev hubport,id=port3,hubid=0,netdev=peer3"
    ] else [
      "-netdev socket,id=cluster,connect=127.0.0.1:${toString (23220 + vmIndex)}"
    ]);
    forwardPorts = [
      { from = "host"; host.address = "127.0.0.1"; host.port = 22220 + vmIndex; guest.port = 22; }
      { from = "host"; host.address = "127.0.0.1"; host.port = 18080 + vmIndex; guest.port = 8080; }
    ];
  };
  system.stateVersion = "26.05";
}
