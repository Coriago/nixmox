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
      installers = import ./nix/installers { inherit self nixpkgs systems; };
    in
    {
      packages = each (
        system:
        let
          pkgs = nixpkgs.legacyPackages.${system};
          vendorHash = "sha256-1Ya+vSHR1DJiTPZhUiEb3bIBnOctACtyaIzZqWAIkkI=";
        in
        {
          installer = installers.${system}.general.iso.config.system.build.isoImage;
          installer-vm = installers.${system}.vm.iso.config.system.build.isoImage;
          installer-kexec = installers.${system}.general.kexec.config.system.build.kexecTree;
          installer-vm-kexec = installers.${system}.vm.kexec.config.system.build.kexecTree;
          default = pkgs.buildGoModule {
            pname = "nixmox";
            version = "0.1.0-dev";
            src = self;
            inherit vendorHash;
            nativeCheckInputs = [
              pkgs.sops
              pkgs.ssh-to-age
            ];
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
              just
              nushell # CLI-driven VM E2E scripts.
              openssh
              procps # Verify disposable VM session ownership before cleanup.
              util-linux # Isolate disposable VM processes in their own session.
              nodejs # Execute the shared progress controller tests under Go/WASM.
              python3 # Small PTY probe; no application runtime dependency.
              git
              sops
              ssh-to-age
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
      nixosModules.installer = {
        imports = [
          self.nixosModules.default
          ./nix/installers/general.nix
        ];
      };
      nixosModules.installer-vm = {
        imports = [
          self.nixosModules.default
          ./nix/installers/vm.nix
        ];
      };
      nixosConfigurations = nixpkgs.lib.foldlAttrs (
        configs: system: profiles:
        configs
        // nixpkgs.lib.foldlAttrs (
          configs: profile: formats:
          configs
          // nixpkgs.lib.mapAttrs' (
            format: machine: nixpkgs.lib.nameValuePair "installer-${profile}-${system}-${format}" machine
          ) formats
        ) { } profiles
      ) { } installers;
    };
}
