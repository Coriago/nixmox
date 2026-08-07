{
  description = "nixmox — Proxmox-like management daemon for NixOS";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs = { self, nixpkgs }:
    let
      system = "x86_64-linux";
      pkgs = nixpkgs.legacyPackages.${system};
      lib = nixpkgs.lib;

      nixmox = pkgs.callPackage ./nix/package.nix {
        ldflags = [
          "-X nixmox/internal/config.Version=0.0.1"
          "-X nixmox/internal/config.Commit=${self.rev or self.dirtyRev or "unknown"}"
        ];
      };

      mkNodeConfig = name: {
        networking.hostName = name;
        system.stateVersion = "24.11";

        boot.loader.systemd-boot.enable = true;
        boot.loader.efi.canTouchEfiVariables = false;

        fileSystems."/" = {
          device = "/dev/vda1";
          fsType = "ext4";
        };
      };
    in
    {
      packages.${system} = {
        default = nixmox;
        demo = import ./nix/demo.nix { inherit pkgs self; };
      };

      devShells.${system}.default = pkgs.mkShell {
        packages = with pkgs; [
          go
          gopls
          golangci-lint
          just
        ];
      };

      nixosModules.default = import ./nix/module.nix;

      checks.${system} = {
        gitops-apply = import ./nix/checks/gitops-apply.nix {
          inherit self pkgs;
        };
        gitops-pull = import ./nix/checks/gitops-pull.nix {
          inherit self pkgs;
        };
        jobs-runner = import ./nix/checks/jobs-runner.nix {
          inherit self pkgs;
        };
        single-node-portal = import ./nix/checks/single-node-portal.nix {
          inherit self pkgs;
        };
      };

      nixosConfigurations = {
        node1 = lib.nixosSystem {
          inherit system;
          modules = [
            self.nixosModules.default
            (mkNodeConfig "node1")
            (import ./nix/vm-common.nix { name = "node1"; index = 1; package = nixmox; })
          ];
        };
        node2 = lib.nixosSystem {
          inherit system;
          modules = [
            self.nixosModules.default
            (mkNodeConfig "node2")
            (import ./nix/vm-common.nix { name = "node2"; index = 2; package = nixmox; })
          ];
        };
        node3 = lib.nixosSystem {
          inherit system;
          modules = [
            self.nixosModules.default
            (mkNodeConfig "node3")
            (import ./nix/vm-common.nix { name = "node3"; index = 3; package = nixmox; })
          ];
        };
      };

      apps.${system}.demo = {
        type = "app";
        program = "${self.packages.${system}.demo}/bin/nixmox-demo";
      };
    };
}
