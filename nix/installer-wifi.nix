{ config, pkgs, ... }:
{
  networking.networkmanager.enable = true;
  environment.systemPackages = [ pkgs.networkmanager ];

  # Own the first local console until the selector exits, then allow the usual
  # installer login. Networking and nixmox start independently of this prompt.
  systemd.services.nixmox-installer = {
    description = "Nixmox installer terminal interface";
    wantedBy = [ "multi-user.target" ];
    wants = [ "NetworkManager.service" ];
    after = [ "NetworkManager.service" ];
    before = [ "getty@tty1.service" ];
    unitConfig.ConditionPathExists = "/dev/tty1";
    environment.TERM = "linux";
    path = [ pkgs.networkmanager ];
    serviceConfig = {
      Type = "oneshot";
      ExecStart = "${config.services.nixmox.package}/bin/nixmox installer --auto";
      RemainAfterExit = true;
      StandardInput = "tty";
      StandardOutput = "tty";
      StandardError = "tty";
      TTYPath = "/dev/tty1";
      TTYReset = true;
      TTYVHangup = true;
      TimeoutStartSec = "infinity";
    };
  };
}
