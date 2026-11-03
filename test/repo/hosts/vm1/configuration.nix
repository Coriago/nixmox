{ ... }:
{
  imports = [
    (import ../../modules/host.nix (builtins.fromJSON (builtins.readFile ./settings.json)))
    ./hardware.nix
    ./disk.nix
  ];
}
