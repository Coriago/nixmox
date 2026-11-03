{ ... }:
{
  # qemu-vm creates a private partitioned root disk and installs its bootloader.
  # Never attach a host block device to this fixture.
  virtualisation.useDefaultFilesystems = true;
  boot.growPartition = true;
  virtualisation.fileSystems."/".autoResize = true;
  boot.loader.grub.enable = true;
  boot.loader.grub.device = "/dev/vda";
}
