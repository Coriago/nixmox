{
  lib,
  pkgs,
  modulesPath,
  ...
}:
{
  imports = [
    ./base.nix
    (modulesPath + "/profiles/qemu-guest.nix")
  ];

  services.qemuGuest.enable = true;
  boot.kernelParams = [
    "console=tty0"
  ]
  ++ lib.optional pkgs.stdenv.hostPlatform.isx86_64 "console=ttyS0,115200"
  ++ lib.optional pkgs.stdenv.hostPlatform.isAarch64 "console=ttyAMA0,115200";
}
