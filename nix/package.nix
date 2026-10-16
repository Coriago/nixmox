{ lib, buildGoModule, ldflags ? [] }:

buildGoModule {
  pname = "nixmox";
  version = "0.2.0";
  src = ../.;
  vendorHash = "sha256-x5At9bkzn2ziWnGnaT149PeVBiHO6JSZIdMtHWtBvfk=";
  subPackages = [ "cmd/nixmox" ];
  inherit ldflags;

  meta = with lib; {
    description = "Proxmox-like management daemon for NixOS";
    license = licenses.mit;
    platforms = platforms.linux;
  };
}
