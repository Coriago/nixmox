{ self, pkgs, ... }:

pkgs.testers.runNixOSTest {
  name = "gitops-apply";

  nodes.machine = { config, pkgs, ... }: {
    imports = [ self.nixosModules.default ];
    services.nixmox.enable = true;
    services.nixmox.package = self.packages.${pkgs.system}.default;
    services.nixmox.gitops.repoUrl = "file:///srv/gitops-fixture.git";
    services.nixmox.gitops.pollIntervalSeconds = 1;
    services.nixmox.applyMode = "boot";

    environment.systemPackages = [ pkgs.git pkgs.jq ];
    environment.etc."nixpkgs".source = pkgs.path;

    nix.settings.experimental-features = [ "nix-command" "flakes" ];

    virtualisation.memorySize = 4096;
    virtualisation.cores = 2;
  };

  testScript = ''
    import json

    machine.wait_for_unit("nixmox.service")
    machine.wait_for_open_port(8080)

    machine.succeed("git init -q --bare --initial-branch=main /srv/gitops-fixture.git")
    machine.succeed("git init -q --initial-branch=main /srv/work")
    machine.succeed("git -C /srv/work remote add origin /srv/gitops-fixture.git")
    machine.succeed("git -C /srv/work config user.email t@t && git -C /srv/work config user.name t")

    flake = """
{
  inputs.nixpkgs.url = "path:/etc/nixpkgs";
  outputs = { nixpkgs, ... }: {
    nixosConfigurations.machine = nixpkgs.lib.nixosSystem {
      system = "x86_64-linux";
      modules = [
        ({ ... }: {
          networking.hostName = "machine";
          system.stateVersion = "24.11";
          boot.loader.grub.device = "nodev";
          fileSystems."/" = { device = "/dev/vda1"; fsType = "ext4"; };
          environment.etc."apply-marker".text = "v1";
        })
      ];
    };
  };
}
"""
    machine.succeed("mkdir -p /srv/work")
    with open("/tmp/flake.nix", "w") as f:
        f.write(flake)
    machine.copy_from_host("/tmp/flake.nix", "/srv/work/flake.nix")
    machine.succeed("git -C /srv/work add . && git -C /srv/work commit -q -m 'flake v1' && git -C /srv/work push -q origin main")

    def gitops():
        return json.loads(machine.succeed("curl -sf http://127.0.0.1:8080/api/v1/gitops"))

    machine.wait_until_succeeds(
        "curl -sf http://127.0.0.1:8080/api/v1/gitops | jq -e '.last_pulled_sha != null and .last_pulled_sha != \"\"'",
        timeout=60,
    )

    machine.succeed("curl -sf -XPOST http://127.0.0.1:8080/api/v1/apply")

    machine.wait_until_succeeds(
        "curl -sf http://127.0.0.1:8080/api/v1/hosts/machine | jq -e '.apply_record != null and .apply_record.status == \"success\"'",
        timeout=900,
    )

    rec = json.loads(machine.succeed("curl -sf http://127.0.0.1:8080/api/v1/hosts/machine"))["apply_record"]
    assert rec["gen_after"] > rec["gen_before"], f"expected gen bump, got {rec}"
    assert len(rec["git_sha"]) >= 7, f"expected git sha, got {rec}"

    machine.succeed("test -f /var/lib/nixmox/applylog/machine.jsonl")
    machine.wait_until_succeeds(
        "curl -sf http://127.0.0.1:8080/api/v1/hosts/machine/applylog | jq -e 'length >= 1'",
        timeout=30,
    )

    machine.succeed("sed -i 's/v1/v2/' /srv/work/flake.nix")
    machine.succeed("git -C /srv/work add . && git -C /srv/work commit -q -m 'flake v2' && git -C /srv/work push -q origin main")

    machine.wait_until_succeeds(
        "curl -sf http://127.0.0.1:8080/api/v1/gitops | jq -e '.update_available == true'",
        timeout=60,
    )

    machine.succeed("curl -sf -XPOST http://127.0.0.1:8080/api/v1/apply")
    machine.wait_until_succeeds(
        "curl -sf http://127.0.0.1:8080/api/v1/hosts/machine/applylog | jq -e 'length >= 2 and .[0].status == \"success\"'",
        timeout=900,
    )

    machine.succeed("curl -sf -XPOST http://127.0.0.1:8080/api/v1/rollback")
    machine.wait_until_succeeds(
        "curl -sf http://127.0.0.1:8080/api/v1/hosts/machine/applylog | jq -e 'length >= 3 and .[0].status == \"success\"'",
        timeout=300,
    )
  '';
}
