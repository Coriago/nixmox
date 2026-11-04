{
  description = "Nixmox CLI E2E GitOps fixture";
  inputs.nixmox.url = "github:Coriago/nixmox/dev/e2e-phase2";
  inputs.nixpkgs.follows = "nixmox/nixpkgs";
  inputs.disko.url = "github:nix-community/disko";
  inputs.disko.inputs.nixpkgs.follows = "nixpkgs";
  outputs =
    {
      nixpkgs,
      nixmox,
      disko,
      ...
    }:
    let
      # Recipe-generated hosts become configurations without editing the flake.
      directories = nixpkgs.lib.filterAttrs (
        name: type: type == "directory" && builtins.pathExists (./hosts + "/${name}/configuration.nix")
      ) (builtins.readDir ./hosts);
      hosts = builtins.mapAttrs (
        name: _:
        nixpkgs.lib.nixosSystem {
          specialArgs = { inherit nixmox disko; };
          modules = [ (./hosts + "/${name}/configuration.nix") ];
        }
      ) directories;
      unjoined = nixpkgs.lib.nixosSystem {
        system = "x86_64-linux";
        specialArgs = { inherit nixmox; };
        modules = [ ./modules/unjoined.nix ];
      };
      installer = nixpkgs.lib.nixosSystem {
        system = "x86_64-linux";
        specialArgs = { inherit nixmox; };
        modules = [ ./modules/installer.nix ];
      };
    in
    {
      nixosConfigurations = hosts;
      configurations.nixos = hosts;
      modules.nixos.testpkg = import ./modules/testpkg.nix;
      packages.x86_64-linux = {
        vm1 = hosts.vm1.config.system.build.vm;
        installer = installer.config.system.build.vm;
        unjoined = unjoined.config.system.build.vm;
      };
    };
}
