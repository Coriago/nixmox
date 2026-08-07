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
    gitops.pollIntervalSeconds = 15;
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
    virtualisation.memorySize = 2048;
    virtualisation.cores = 2;
    virtualisation.graphics = false;
  };
}
