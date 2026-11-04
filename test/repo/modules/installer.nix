{ nixmox, ... }:
{
  imports = [
    nixmox.nixosModules.installer-vm
    ./base.nix
    ./vm.nix
  ];
  # One image for any target; DHCP and normal installer defaults supply boot networking.
  services.nixmox.config.listen = "0.0.0.0:8080";
  # Firmware boots the blank target first, then this persistent rescue disk.
  # After installation the same reboot selects the target's real bootloader.
  virtualisation.useBootLoader = true;
}
