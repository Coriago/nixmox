{ config, vmIndex, nixmoxModule, ... }:
{
  imports = [ nixmoxModule ];
  services.nixmox = {
    enable = true;
    config = {
      name = config.networking.hostName;
      nats-host = "192.168.77.${toString vmIndex}";
      listen = "0.0.0.0:8080";
      advertise = "http://192.168.77.${toString vmIndex}:8080";
    };
  };
  systemd.services.nixmox.requires = [ "sshd-keygen.service" ];
}
