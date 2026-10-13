{
  description = "nixmox — Proxmox-like management daemon for NixOS";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs = { self, nixpkgs }:
    let
      system = "x86_64-linux";
      pkgs = nixpkgs.legacyPackages.${system};
      lib = nixpkgs.lib;

      mkPackage = target: nixpkgs.legacyPackages.${target}.callPackage ./nix/package.nix {
        ldflags = [
          "-X nixmox/internal/config.Version=0.0.1"
          "-X nixmox/internal/config.Commit=${self.rev or self.dirtyRev or "unknown"}"
        ];
      };

      nixmox = mkPackage system;

      mkNodeConfig = name: {
        networking.hostName = name;
        system.stateVersion = "24.11";

        boot.loader.grub.device = "nodev";

        fileSystems."/" = {
          device = "/dev/vda1";
          fsType = "ext4";
        };
      };
    in
    {
      packages.${system} = {
        default = nixmox;
        github-e2e = pkgs.writeShellApplication {
          name = "nixmox-github-e2e";
          runtimeInputs = [ pkgs.curl pkgs.jq pkgs.coreutils ];
          runtimeEnv.NIXMOX_E2E_VM = self.nixosConfigurations.node1.config.system.build.vm;
          text = builtins.readFile ./e2e/run.sh;
        };
        demo = import ./nix/demo.nix { inherit pkgs self; };
        installer-iso = (self.lib.mkInstaller {
          inherit system;
          format = "iso";
        }).config.system.build.isoImage;
      };

      lib.mkInstaller = { system ? "x86_64-linux", format ? "iso", sshKeys ? [] }:
        assert lib.assertMsg (builtins.elem format [ "iso" "sd" ]) "installer format must be iso or sd";
        assert lib.assertMsg (format != "sd" || system == "aarch64-linux") "the generic SD profile requires aarch64-linux";
        nixpkgs.lib.nixosSystem {
          inherit system;
          modules = [
            (if format == "iso"
             then "${nixpkgs}/nixos/modules/installer/cd-dvd/installation-cd-minimal.nix"
             else "${nixpkgs}/nixos/modules/installer/sd-card/sd-image-aarch64.nix")
            (import ./nix/installer.nix {
              package = self.packages.${system}.default;
              inherit sshKeys;
            })
          ];
        };

      packages.aarch64-linux.default = mkPackage "aarch64-linux";

      packages.aarch64-linux.installer-sd = (self.lib.mkInstaller {
        system = "aarch64-linux";
        format = "sd";
      }).config.system.build.sdImage;

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
        cluster-lifecycle = import ./nix/checks/cluster-lifecycle.nix {
          inherit self pkgs;
        };
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

      apps.${system} = {
        github-e2e = {
          type = "app";
          meta.description = "Boot a disposable VM and apply the e2e flake pulled from GitHub main";
          program = "${self.packages.${system}.github-e2e}/bin/nixmox-github-e2e";
        };
        demo = {
          meta.description = "Run the three-node nixmox demo";
          type = "app";
          program = "${self.packages.${system}.demo}/bin/nixmox-demo";
        };
      };
    };
}
