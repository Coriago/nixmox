{
  description = "Nixmox CLI E2E GitOps fixture";
  inputs.nixmox.url = "github:Coriago/nixmox/dev/e2e-phase1";
  inputs.nixpkgs.follows = "nixmox/nixpkgs";
  outputs = { nixpkgs, nixmox, ... }:
    let
      vm1 = nixpkgs.lib.nixosSystem {
        system = "x86_64-linux";
        specialArgs = { inherit nixmox; };
        modules = [ ./hosts/vm1/configuration.nix ];
      };
    in {
      nixosConfigurations.vm1 = vm1;
      configurations.nixos.vm1 = vm1;
      packages.x86_64-linux.vm1 = vm1.config.system.build.vm;
    };
}
