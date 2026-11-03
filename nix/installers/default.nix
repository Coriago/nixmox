{
  self,
  nixpkgs,
  systems,
}:
let
  lib = nixpkgs.lib;
  profiles = {
    general = ./general.nix;
    vm = ./vm.nix;
  };
  formats = {
    iso = { pkgs, modulesPath, ... }: {
      imports = [ (modulesPath + "/installer/cd-dvd/iso-image.nix") ];
      isoImage = {
        makeEfiBootable = true;
        makeBiosBootable = pkgs.stdenv.hostPlatform.isx86_64;
        makeUsbBootable = true;
      };
    };
    kexec = { modulesPath, ... }: {
      imports = [ (modulesPath + "/installer/netboot/netboot.nix") ];
    };
  };
in
lib.genAttrs systems (
  system:
  lib.mapAttrs (
    profile: module:
    lib.mapAttrs (
      format: bootModule:
      lib.nixosSystem {
        inherit system;
        modules = [
          self.nixosModules.default
          module
          bootModule
          (
            {
              networking.hostName = lib.mkDefault "nixmox-installer";
            }
            // lib.optionalAttrs (format == "iso") {
              isoImage = {
                edition = "nixmox-${profile}";
                volumeID = "nixmox-${profile}-${system}";
              };
            }
          )
        ];
      }
    ) formats
  ) profiles
)
