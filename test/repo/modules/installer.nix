{ nixmox, ... }:
{
  imports = [
    nixmox.nixosModules.installer-vm
    ./base.nix
    ./vm.nix
  ];
  # One image for any target; DHCP and normal installer defaults supply boot networking.
  services.nixmox.config.listen = "0.0.0.0:8080";
  virtualisation = {
    useBootLoader = false;
    diskImage = null;
    useNixStoreImage = true;
    writableStore = true;
  };
}
