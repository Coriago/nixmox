{
  description = "nixmox GitHub end-to-end deployment fixture (disposable VMs only)";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    # Use the agent and module from the very same pulled Git commit. A second
    # GitHub input would accidentally test a different nixmox revision.
    nixmox.url = "path:..";
    nixmox.inputs.nixpkgs.follows = "nixpkgs";
  };

  outputs = { nixpkgs, nixmox, ... }: {
    nixosConfigurations = builtins.listToAttrs (map (index: {
      name = "node${toString index}";
      value = nixpkgs.lib.nixosSystem {
        system = "x86_64-linux";
        specialArgs = { inherit index nixmox; };
        modules = [ ./configuration.nix ];
      };
    }) [ 1 2 3 ]);
  };
}
