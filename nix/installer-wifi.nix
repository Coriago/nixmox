{ pkgs, ... }:
let
  wifi = pkgs.writeShellApplication {
    name = "nixmox-wifi";
    runtimeInputs = [ pkgs.networkmanager pkgs.iproute2 pkgs.jq pkgs.coreutils pkgs.gnugrep ];
    text = builtins.readFile ./installer-wifi.sh;
  };
in {
  networking.networkmanager.enable = true;
  environment.systemPackages = [ wifi ];

  # Own the first local console until the selector exits, then allow the usual
  # installer login. Networking and nixmox start independently of this prompt.
  systemd.services.nixmox-wifi = {
    description = "Connect the nixmox installer to Wi-Fi";
    wantedBy = [ "multi-user.target" ];
    wants = [ "NetworkManager.service" ];
    after = [ "NetworkManager.service" ];
    before = [ "getty@tty1.service" ];
    unitConfig.ConditionPathExists = "/dev/tty1";
    environment.TERM = "linux";
    serviceConfig = {
      Type = "oneshot";
      ExecStart = "${wifi}/bin/nixmox-wifi --auto";
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
