{ lib, pkgs, modulesPath, index, nixmox, ... }:

{
  imports = [
    # Keep the VM filesystem, networking and console during a real switch.
    # These are deployment configurations, not prebuilt closure selectors.
    "${modulesPath}/virtualisation/qemu-vm.nix"
    nixmox.nixosModules.default
    (import "${nixmox}/nix/vm-common.nix" {
      name = "node${toString index}";
      inherit index;
      package = nixmox.packages.${pkgs.stdenv.hostPlatform.system}.default;
    })
  ];

  system.stateVersion = "24.11";
  boot.loader.grub.enable = lib.mkForce true;
  virtualisation = {
    useBootLoader = true;
    memorySize = 4096;
    diskSize = 16384;
    useNixStoreImage = true;
    writableStore = true;
    writableStoreUseTmpfs = false;
    cores = 2;
    graphics = false;
  };

  services.nixmox = {
    autoApply = false;
    applyMode = "switch";
    enableCustomTasks = true;
    gitops = {
      # Only explicit snapshot runs generate this file, inside their isolated Git repo.
      repoUrl = lib.mkForce (if builtins.pathExists ./source.json
        then (builtins.fromJSON (builtins.readFile ./source.json)).repo_url
        else "https://github.com/Coriago/nixmox.git");
      branch = "main";
      flakeDirectory = "e2e";
      flakeAttribute = "node${toString index}";
      pollIntervalSeconds = lib.mkForce 0;
    };
  };

  # Neither the marker, package nor unit exists in the bootstrap demo VM.
  # The E2E runner checks their absence before applying and presence afterward.
  environment.etc."nixmox-e2e-generation".text = "github-main-e2e-v1\n";
  environment.systemPackages = [ pkgs.hello ];
  systemd.services.nixmox-e2e-proof = {
    description = "Prove the GitHub fixture was activated";
    wantedBy = [ "multi-user.target" ];
    serviceConfig = {
      Type = "oneshot";
      RemainAfterExit = true;
      StateDirectory = "nixmox-e2e";
    };
    script = ''
      ${pkgs.coreutils}/bin/cp /etc/nixmox-e2e-generation /var/lib/nixmox-e2e/activated
      ${pkgs.hello}/bin/hello > /var/lib/nixmox-e2e/hello
    '';
  };
}
