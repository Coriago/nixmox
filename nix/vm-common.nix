{ name, index, package }:

{ lib, pkgs, ... }:

let
  mac = "52:54:00:12:34:0${toString index}";
  ip = "192.168.42.${toString index}";
in
{
  # The base configuration is only used to construct the VM variant below.
  boot.loader.grub.enable = false;
  boot.growPartition = true;

  # qemu-vm overrides the root filesystem declaration. Grow ext4 explicitly
  # after growpart, before builds can fill the small bootstrap image filesystem.
  systemd.services.nixmox-vm-grow-root = {
    wantedBy = [ "multi-user.target" ];
    requires = [ "growpart.service" ];
    after = [ "growpart.service" ];
    before = [ "nixmox.service" ];
    serviceConfig = { Type = "oneshot"; RemainAfterExit = true; };
    script = "${pkgs.e2fsprogs}/bin/resize2fs /dev/disk/by-label/nixos";
  };

  networking.hostName = name;

  networking.useDHCP = true;
  systemd.network.links."10-cluster" = {
    matchConfig.MACAddress = mac;
    linkConfig.Name = "cluster0";
  };
  networking.interfaces.cluster0 = {
    useDHCP = false;
    ipv4.addresses = [ { address = ip; prefixLength = 24; } ];
  };

  services.nixmox = {
    enable = true;
    inherit package;
    advertiseAddr = ip;
    allowConfigWrite = true;
    enableCustomTasks = true;
    discovery = true;
    autoApply = false;
    gitops = {
      repoUrl = "https://github.com/Coriago/nixmox.git";
      branch = "main";
      flakeDirectory = "e2e";
      flakeAttribute = name;
      pollIntervalSeconds = 0;
    };
  };

  services.openssh = {
    enable = true;
    settings.PermitRootLogin = "yes";
  };
  users.users.root.initialPassword = "nixos";

  virtualisation.vmVariant = {
    boot.loader.grub.enable = lib.mkForce true;
    virtualisation.useBootLoader = true;
    virtualisation.forwardPorts = [
      { from = "host"; host.port = 8080 + index; guest.port = 8080; }
      { from = "host"; host.port = 2220 + index; guest.port = 22; }
    ];
    virtualisation.memorySize = 4096;
    virtualisation.diskSize = 16384;
    # Seed only the bootstrap closure, rather than exposing the host's entire
    # Nix store to the guest's deployment build.
    virtualisation.useNixStoreImage = true;
    virtualisation.writableStore = true;
    virtualisation.writableStoreUseTmpfs = false;
    virtualisation.cores = 2;
    virtualisation.graphics = false;
  };
}
