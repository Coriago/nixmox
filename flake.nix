{
  description = "Nixmox cluster lifecycle manager";
  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
  outputs =
    { self, nixpkgs }:
    let
      systems = [
        "x86_64-linux"
        "aarch64-linux"
      ];
      each = nixpkgs.lib.genAttrs systems;
    in
    {
      packages = each (
        system:
        let
          pkgs = nixpkgs.legacyPackages.${system};
        in
        {
          default = pkgs.buildGoModule {
            pname = "nixmox";
            version = "0.1.0-dev";
            src = self;
            vendorHash = "sha256-XmP0H3BkJamNIK0iFJPUOM3xFHTdki3EoBiwJH32AZs=";
            subPackages = [ "cmd/nixmox" ];
            env.CGO_ENABLED = "0"; # Bootstrap agents must not require a host libc.
            nativeCheckInputs = [ pkgs.git ];
            checkPhase = ''
              runHook preCheck
              go test ./... -count=1 -timeout=120s
              runHook postCheck
            '';
          };
        }
      );
      devShells = each (
        system:
        let
          pkgs = nixpkgs.legacyPackages.${system};
        in
        {
          default = pkgs.mkShell {
            packages = with pkgs; [
              go
              gopls
              git
              nixfmt
              ripgrep
            ];
            GOTOOLCHAIN = "local";
            CGO_ENABLED = "0";
          };
        }
      );
      formatter = each (system: nixpkgs.legacyPackages.${system}.nixfmt);
      nixosModules.default = import ./nix/module.nix self;
      nixosModules.installer = import ./nix/installer.nix self;
      # Board exports are gated on pinned recipes and physical validation.
    };
}
