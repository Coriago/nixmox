{
  description = "Nixmox node management";
  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
  outputs =
    { self, nixpkgs }:
    let
      systems = [
        "x86_64-linux"
        "aarch64-linux"
      ];
      eachSystem = nixpkgs.lib.genAttrs systems;
    in
    {
      packages = eachSystem (
        system:
        let
          pkgs = import nixpkgs { inherit system; };
        in
        {
          default = pkgs.buildGoModule {
            pname = "nixmox";
            version = "0.1.0-dev";
            src = self;
            vendorHash = "sha256-vZPhdLVUOQuMDDnc33Vyu3y0KkNML7Y4axExl+uDDMw=";
            subPackages = [ "cmd/nixmox" ];
            env.CGO_ENABLED = 0;
            checkPhase = ''
              runHook preCheck
              go test ./...
              runHook postCheck
            '';
            meta.mainProgram = "nixmox";
          };
        }
      );
      nixosModules.default = { lib, pkgs, ... }: {
        imports = [ ./nix/module.nix ];
        services.nixmox.package = lib.mkDefault self.packages.${pkgs.stdenv.hostPlatform.system}.default;
      };
      checks = eachSystem (
        system:
        let
          pkgs = import nixpkgs { inherit system; };
          evaluated = nixpkgs.lib.nixosSystem {
            inherit system;
            modules = [
              self.nixosModules.default
              {
                services.nixmox = {
                  enable = true;
                  http.port = 63200;
                  network.managementInterfaces = [ "eth0" ];
                };
                boot.isContainer = true;
                boot.kernel.sysctl."net.ipv4.ip_local_reserved_ports" = "50000-50010";
                system.stateVersion = "26.05";
              }
            ];
          };
          c = evaluated.config;
        in
        {
          package = self.packages.${system}.default;
          bootstrap-vm = import ./tests/vm/bootstrap.nix {
            inherit pkgs;
            module = self.nixosModules.default;
          };
          module =
            assert c.services.nixmox.http.port == 63200;
            assert
              c.networking.firewall.interfaces.eth0.allowedTCPPorts == [
                63172
                63173
                63175
                63200
              ];
            assert c.networking.firewall.allowedTCPPorts == [ ];
            assert builtins.elem "63200" (
              nixpkgs.lib.splitString "," c.boot.kernel.sysctl."net.ipv4.ip_local_reserved_ports"
            );
            assert builtins.elem "50000-50010" (
              nixpkgs.lib.splitString "," c.boot.kernel.sysctl."net.ipv4.ip_local_reserved_ports"
            );
            pkgs.runCommand "nixmox-module-check" { nativeBuildInputs = [ self.packages.${system}.default ]; }
              ''
                # Feed the module's generated YAML into the actual typed parser.
                nixmox --config ${c.environment.etc."nixmox/config.yaml".source} identity show 2>error && exit 1
                grep -q OUTCOME_UNKNOWN error
                touch $out
              '';
        }
      );
      devShells = eachSystem (
        system:
        let
          pkgs = import nixpkgs { inherit system; };
        in
        {
          default = pkgs.mkShell {
            packages = with pkgs; [
              go
              git
              curl
              nixfmt
              openssh
              qemu
              iproute2
              util-linux
              nix
            ];
          };
        }
      );
    };
}
