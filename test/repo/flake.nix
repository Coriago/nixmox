{
  description = "Nixmox CLI E2E GitOps fixture";
  inputs.nixmox.url = "github:Coriago/nixmox/dev/e2e-phase2";
  inputs.nixpkgs.follows = "nixmox/nixpkgs";
  outputs = { nixpkgs, nixmox, ... }:
    let
      vm1 = nixpkgs.lib.nixosSystem {
        system = "x86_64-linux";
        specialArgs = { inherit nixmox; vmIndex = 1; nixmoxModule = nixmox.nixosModules.default; };
        modules = [ ./hosts/vm1/configuration.nix ];
      };
      vm2 = nixpkgs.lib.nixosSystem {
        system = "x86_64-linux";
        specialArgs = { inherit nixmox; vmIndex = 2; nixmoxModule = nixmox.nixosModules.installer-vm; };
        modules = [ ./modules/installer.nix ];
      };
    in {
      nixosConfigurations.vm1 = vm1;
      nixosConfigurations.vm2-installer = vm2;
      configurations.nixos.vm1 = vm1;
      packages.x86_64-linux.vm1 = vm1.config.system.build.vm;
      packages.x86_64-linux.vm2-installer = vm2.config.system.build.vm;
    };
}
