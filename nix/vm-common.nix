{ name, index, package }:

{ lib, pkgs, ... }:

let
  mac = "52:54:00:12:34:0${toString index}";
  ip = "192.168.42.${toString index}";
in
{
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
