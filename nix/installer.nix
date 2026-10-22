self:
{
  config,
  lib,
  pkgs,
  ...
}:
{
  imports = [ (import ./module.nix self) ];
  # Shared installer scaffold only. Board/image exports must pin and verify
  # their kernel, firmware, boot adapters and handoff support before use.
  services.nixmox = {
    enable = true;
    dataDir = lib.mkDefault "/run/nixmox/state";
    discovery.enable = lib.mkDefault true;
    capabilities.install = lib.mkDefault true;
  };
  networking.networkmanager.enable = lib.mkDefault true;
  systemd.services.nixmox-installer-console = {
    description = "Nixmox installer terminal";
    wantedBy = [ "multi-user.target" ];
    after = [ "nixmox.service" ];
    requires = [ "nixmox.service" ];
    conflicts = [ "getty@tty1.service" ];
    serviceConfig = {
      ExecStart = "${config.services.nixmox.package}/bin/nixmox tui --installer";
      StandardInput = "tty";
      StandardOutput = "tty";
      TTYPath = "/dev/tty1";
      TTYReset = true;
      TTYVHangup = true;
      Type = "idle";
    };
  };
  # Automatic management-interface discovery, install tools and one-use handoff
  # restoration are implementation gates; do not advertise a bootable recipe yet.
}
