{ config, nixmox, ... }:
{
  imports = [ nixmox.nixosModules.default ];
  services.nixmox = {
    enable = true;
    config = {
      name = config.networking.hostName;
      listen = "0.0.0.0:8080";
    };
  };
  systemd.services.nixmox.requires = [ "sshd-keygen.service" ];
}
