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
          vendorHash = "sha256-4XWb0/mU0VyGIw6zZVgvUtP+dVxCBSQXTw0VmRFWP/g=";
        in
        {
          default = pkgs.buildGoModule {
            pname = "nixmox";
            version = "0.1.0-dev";
            src = self;
            inherit vendorHash;
            subPackages = [ "cmd/nixmox" ];
            env.CGO_ENABLED = "0"; # Bootstrap agents must not require a host libc.
            postBuild = ''
              mkdir -p ui-assets
              GOOS=js GOARCH=wasm go build -trimpath -ldflags="-s -w" -o ui-assets/app.wasm ./cmd/nixmox
              cp "$(go env GOROOT)/lib/wasm/wasm_exec.js" ui-assets/wasm_exec.js
            '';
            postInstall = ''
              mkdir -p $out/share/nixmox-ui
              cp ui-assets/* $out/share/nixmox-ui/
            '';
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
              nodejs # Execute the shared progress controller tests under Go/WASM.
              python3 # Small PTY probe; no application runtime dependency.
              git
              jq # Disposable CLI-only multi-node smoke check.
              greptimedb
              opentelemetry-collector-contrib
              curl
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
