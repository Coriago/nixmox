{ lib, nixmox, ... }:
{
  imports = [
    ./base.nix
    ./nixmox.nix
  ];
  virtualisation = {
    useBootLoader = lib.mkForce false;
    diskImage = lib.mkForce null;
    useNixStoreImage = true;
    writableStore = true;
    # Stored in this VM's private persistent TMPDIR, never a host block device.
    emptyDiskImages = [
      { size = 16384; driveConfig.deviceExtraOpts.serial = "nixmox-vm2"; }
    ];
  };
}
