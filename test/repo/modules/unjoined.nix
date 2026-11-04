# A normal installed environment, independent of the recipe-generated VM3 host.
{ modulesPath, ... }:
{
  imports = [
    (import ./host.nix (builtins.fromJSON (builtins.readFile ../boot/vm3.json)))
    ./vm.nix
    (modulesPath + "/profiles/qemu-guest.nix")
  ];
  nixpkgs.hostPlatform = "x86_64-linux";
  services.nixmox.config.environment = "installed";
  boot.loader.grub.enable = true;
  boot.loader.grub.device = "/dev/vda";
  system.stateVersion = "26.05";
}
