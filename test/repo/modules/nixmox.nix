{ nixmox, ... }:
{
  imports = [ nixmox.nixosModules.default ];
  services.nixmox = {
    enable = true;
    config = {
      name = "vm1";
      listen = "0.0.0.0:8080";
      advertise = "http://127.0.0.1:8080";
    };
  };
  systemd.services.nixmox.requires = [ "sshd-keygen.service" ];
}
