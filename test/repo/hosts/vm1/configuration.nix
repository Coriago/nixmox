{ ... }:
{
  imports = [
    ../../modules/base.nix
    ../../modules/nixmox.nix
    ./hardware.nix
    ./disk.nix
  ];
}
