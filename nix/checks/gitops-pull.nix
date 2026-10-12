{ self, pkgs, ... }:

pkgs.testers.runNixOSTest {
  name = "gitops-pull";

  nodes.machine = { config, pkgs, ... }: {
    imports = [ self.nixosModules.default ];
    services.nixmox.enable = true;
    services.nixmox.package = self.packages.${pkgs.stdenv.hostPlatform.system}.default;
    services.nixmox.gitops.repoUrl = "file:///srv/gitops-fixture.git";
    services.nixmox.gitops.pollIntervalSeconds = 1;

    environment.systemPackages = [ pkgs.git pkgs.jq ];
  };

  testScript = ''
    machine.wait_for_unit("nixmox.service")
    machine.wait_for_open_port(8080)

    machine.succeed("git init -q --bare --initial-branch=main /srv/gitops-fixture.git")
    machine.succeed("git init -q --initial-branch=main /srv/work")
    machine.succeed("git -C /srv/work remote add origin /srv/gitops-fixture.git")
    machine.succeed("git -C /srv/work config user.email t@t && git -C /srv/work config user.name t")
    machine.succeed("echo '{}' > /srv/work/flake.nix && git -C /srv/work add . && git -C /srv/work commit -q -m init && git -C /srv/work push -q origin main")

    machine.wait_until_succeeds(
        "curl -sf http://127.0.0.1:8080/api/v1/gitops | jq -e '.last_pulled_sha != null and .last_pulled_sha != \"\" and .update_available == false'",
        timeout=60,
    )

    machine.succeed("echo change >> /srv/work/flake.nix && git -C /srv/work add . && git -C /srv/work commit -q -m second && git -C /srv/work push -q origin main")
    new_sha = machine.succeed("git -C /srv/work rev-parse HEAD").strip()

    machine.wait_until_succeeds(
        "curl -sf http://127.0.0.1:8080/api/v1/gitops | jq -e '.update_available == true'",
        timeout=60,
    )

    machine.succeed("curl -sf -XPOST http://127.0.0.1:8080/api/v1/gitops/pull")
    machine.wait_until_succeeds(
        f"curl -sf http://127.0.0.1:8080/api/v1/gitops | jq -e '.last_pulled_sha == \"{new_sha}\" and .update_available == false'",
        timeout=60,
    )
  '';
}
