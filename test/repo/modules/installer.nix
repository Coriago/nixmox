{ nixmox, ... }:
{
  imports = [
    nixmox.nixosModules.installer-vm
    ./base.nix
    ./vm.nix
  ];
  # One image for any target; DHCP and normal installer defaults supply boot networking.
  services.nixmox.config.listen = "0.0.0.0:8080";
  # Start from a persistent rescue disk; after SSH readiness, the acceptance
  # harness selects the installation target for the next firmware boot.
  virtualisation.useBootLoader = true;
}
