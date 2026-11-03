{
  config,
  lib,
  pkgs,
  modulesPath,
  ...
}:
{
  imports = [
    (modulesPath + "/profiles/minimal.nix")
    (modulesPath + "/profiles/installation-device.nix")
  ];

  services.nixmox = {
    enable = true;
    config.environment = "installer";
  };
  systemd.services.nixmox.requires = [ "sshd-keygen.service" ];

  # Keep the privileged API local; remote operators can use SSH forwarding.
  services.openssh.settings = {
    PasswordAuthentication = false;
    KbdInteractiveAuthentication = false;
    PermitRootLogin = "prohibit-password";
  };
  services.openssh.hostKeys = [
    {
      path = "/etc/ssh/ssh_host_ed25519_key";
      type = "ed25519";
    }
  ];

  nix.settings.experimental-features = [
    "nix-command"
    "flakes"
  ];
  environment.systemPackages = with pkgs; [
    kexec-tools
    parted
    gptfdisk
    dosfstools
    e2fsprogs
    btrfs-progs
  ];
  services.getty = {
    helpLine = lib.mkForce "Nixmox installer";
    loginProgram = pkgs.writeShellScript "nixmox-installer-console" ''
      exec ${lib.getExe' config.services.nixmox.package "nixmox"} ui \
        --endpoint ${lib.escapeShellArg config.services.nixmox.config.advertise}
    '';
  };
  system.stateVersion = lib.mkDefault lib.trivial.release;
}
