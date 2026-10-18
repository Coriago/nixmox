{ lib, buildGoModule, ldflags ? [] }:

buildGoModule {
  pname = "nixmox";
  version = "0.4.2";
  src = ../.;
  vendorHash = "sha256-SBbrhC/71KblCkz/uuT8JlgERpjD7npCMVXk5xm7B9s=";
  subPackages = [ "cmd/nixmox" ];
  inherit ldflags;

  meta = with lib; {
    description = "Proxmox-like management daemon for NixOS";
    license = licenses.mit;
    platforms = platforms.linux;
  };
}
