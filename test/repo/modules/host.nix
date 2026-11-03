# Host data comes from a configuration, never from VM names or indices.
settings:
{ ... }:
{
  imports = [
    ./base.nix
    ./nixmox.nix
  ];
  networking.hostName = settings.hostName;
  networking.interfaces.cluster0 = {
    useDHCP = false;
    ipv4.addresses = [ { inherit (settings) address prefixLength; } ];
  };
  networking.firewall.trustedInterfaces = [ "cluster0" ];
  systemd.network.links."10-cluster" = {
    matchConfig.MACAddress = settings.mac;
    linkConfig.Name = "cluster0";
  };
  services.nixmox.config = {
    nats-host = settings.address;
    advertise = "http://${settings.address}:8080";
  };
  users.users.root.openssh.authorizedKeys.keys = settings.authorizedKeys;
  system.stateVersion = settings.stateVersion;
}
