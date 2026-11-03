{ modulesPath, ... }:
{
  imports = [
    ../../modules/vm.nix
    (modulesPath + "/profiles/qemu-guest.nix")
  ];
  nixpkgs.hostPlatform = "x86_64-linux";
}
