{ lib, buildGoModule, ldflags ? [] }:

buildGoModule {
  pname = "nixmox";
  version = "0.0.1";
  src = ../.;
  vendorHash = "sha256-Als1RPMXpCk7SSCU2bGm5k46mbCStHMUVUT2wEByDiM=";
  subPackages = [ "cmd/nixmox" ];
  inherit ldflags;

  meta = with lib; {
    description = "Proxmox-like management daemon for NixOS";
    license = licenses.mit;
    platforms = platforms.linux;
  };
}
