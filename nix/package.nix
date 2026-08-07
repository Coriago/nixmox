{ lib, buildGoModule, ldflags ? [] }:

buildGoModule {
  pname = "nixmox";
  version = "0.0.1";
  src = ../.;
  vendorHash = null;
  subPackages = [ "cmd/nixmox" ];
  inherit ldflags;

  meta = with lib; {
    description = "Proxmox-like management daemon for NixOS";
    license = licenses.mit;
    platforms = platforms.linux;
  };
}
