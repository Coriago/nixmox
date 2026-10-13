{ self, pkgs, ... }:
let
  # Build real NixOS generations outside the network-isolated VM. The fixture
  # flake selects these closures, so rebuild/boot/rollback need no downloads.
  generation = marker: (self.inputs.nixpkgs.lib.nixosSystem {
    system = pkgs.stdenv.hostPlatform.system;
    modules = [ {
      nixpkgs.pkgs = pkgs;
      networking.hostName = "machine";
      system.stateVersion = "24.11";
      boot.loader.grub.device = "nodev";
      fileSystems."/" = { device = "/dev/vda1"; fsType = "ext4"; };
      environment.etc."apply-marker".text = marker;
    } ];
  }).config.system.build.toplevel;
  v1 = generation "v1";
  v2 = generation "v2";
in
pkgs.testers.runNixOSTest {
  name = "gitops-apply";
  nodes.machine = { ... }: {
    imports = [ self.nixosModules.default ];
    services.nixmox = {
      enable = true;
      package = self.packages.${pkgs.stdenv.hostPlatform.system}.default;
      gitops.repoUrl = "file:///srv/gitops-fixture.git";
      gitops.pollIntervalSeconds = 1;
      applyMode = "boot";
    };
    environment.systemPackages = [ pkgs.git pkgs.jq ];
    virtualisation.additionalPaths = [ v1 v2 pkgs.nixos-rebuild ];
    virtualisation.memorySize = 2048;
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
      outputs = { self }: {
        nixosConfigurations.machine.config.system.build = {
          toplevel = builtins.storePath "${v1}";
          nixos-rebuild = builtins.storePath "${pkgs.nixos-rebuild}";
        };
      };
    }
    """
    with open("/tmp/flake.nix", "w") as f:
        f.write(flake)
    machine.copy_from_host("/tmp/flake.nix", "/srv/work/flake.nix")
    machine.succeed("git -C /srv/work add . && git -C /srv/work commit -q -m 'flake v1' && git -C /srv/work push -q origin main")

    def run_job(kind):
        job_id = json.loads(machine.succeed(f"curl -sf -XPOST http://127.0.0.1:8080/api/v1/{kind}"))["id"]
        url = f"http://127.0.0.1:8080/api/v1/jobs/{job_id}"
        machine.wait_until_succeeds(
            f"curl -sf {url} | jq -e '.state == \"applied\" or .state == \"failed\"'",
            timeout=120,
        )
        result = json.loads(machine.succeed(f"curl -sf {url}"))
        assert result["state"] == "applied", result["log"]

    machine.wait_until_succeeds(
        "curl -sf localhost:8080/api/v1/gitops | jq -e '.last_pulled_sha != \"\"'",
        timeout=60,
    )
    run_job("apply")
    machine.wait_until_succeeds(
        "curl -sf localhost:8080/api/v1/hosts/machine | jq -e '.apply_record.status == \"success\"'",
        timeout=30,
    )
    record = json.loads(machine.succeed("curl -sf localhost:8080/api/v1/hosts/machine"))["apply_record"]
    assert record["gen_after"] > record["gen_before"], record
    assert len(record["git_sha"]) >= 7, record
    machine.succeed("test $(readlink -f /nix/var/nix/profiles/system) = ${v1}")

    machine.succeed("sed -i 's|${v1}|${v2}|' /srv/work/flake.nix")
    machine.succeed("git -C /srv/work add . && git -C /srv/work commit -q -m 'flake v2' && git -C /srv/work push -q origin main")
    machine.wait_until_succeeds(
        "curl -sf localhost:8080/api/v1/gitops | jq -e '.update_available == true'",
        timeout=60,
    )
    machine.succeed("curl -sf localhost:8080/api/v1/gitops/preview | jq -e '.files | contains(\"flake.nix\")'")
    run_job("gitops/pull")
    run_job("apply")
    machine.succeed("test $(readlink -f /nix/var/nix/profiles/system) = ${v2}")
    run_job("rollback")
    machine.succeed("test $(readlink -f /nix/var/nix/profiles/system) = ${v1}")
    machine.wait_until_succeeds(
        "curl -sf localhost:8080/api/v1/hosts/machine/applylog | jq -e 'length >= 3 and .[0].status == \"success\"'",
        timeout=30,
    )
  '';
}
