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
          vendorHash = "sha256-YkFDcq2gM4gVw9ICZ4ZFdcSyTURnKkAfzMT6ewVTOsM=";
        in
        {
          default = pkgs.buildGoModule {
            pname = "nixmox";
            version = "0.1.0-dev";
            src = self;
            inherit vendorHash;
            subPackages = [ "cmd/nixmox" ];
            env.CGO_ENABLED = "0"; # Bootstrap agents must not require a host libc.
            nativeCheckInputs = [ pkgs.git ];
            checkPhase = ''
              runHook preCheck
              go test ./... -count=1 -timeout=120s
              runHook postCheck
            '';
          };
          progress-preview = pkgs.buildGoModule {
            pname = "nixmox-progress-preview";
            meta.mainProgram = "progress-preview";
            version = "0.1.0";
            src = self;
            inherit vendorHash;
            subPackages = [ "cmd/progress-preview" ];
            env.CGO_ENABLED = "0";
            nativeCheckInputs = [ pkgs.nodejs ];
            postBuild = ''
              mkdir -p preview-assets
              GOOS=js GOARCH=wasm go build -trimpath -ldflags="-s -w" -o preview-assets/app.wasm ./cmd/progress-preview
              cp "$(go env GOROOT)/lib/wasm/wasm_exec.js" preview-assets/wasm_exec.js
              gzip -k -9 preview-assets/app.wasm
            '';
            checkPhase = ''
              runHook preCheck
              go test ./internal/ui/progress/... -count=1
              GOOS=js GOARCH=wasm go test -exec "env -i PATH=$PATH HOME=$HOME $(go env GOROOT)/lib/wasm/go_js_wasm_exec" ./internal/ui/progress -count=1
              runHook postCheck
            '';
            postInstall = ''
              mkdir -p $out/share/nixmox-progress
              cp preview-assets/* $out/share/nixmox-progress/
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
