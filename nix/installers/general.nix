{ ... }:
{
  imports = [ ./base.nix ];
  hardware.enableAllHardware = true;
  hardware.enableRedistributableFirmware = true;
}
