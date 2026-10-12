{ self, pkgs, ... }:

pkgs.testers.runNixOSTest {
  name = "single-node-portal";

  nodes.machine = { config, pkgs, ... }: {
    imports = [ self.nixosModules.default ];
    services.nixmox.enable = true;
    services.nixmox.package = self.packages.${pkgs.stdenv.hostPlatform.system}.default;
  };

  testScript = ''
    machine.wait_for_unit("nixmox.service")
    machine.wait_for_open_port(8080)
    machine.succeed("curl -sf http://127.0.0.1:8080/healthz | grep -q ok")
    machine.succeed("curl -sf http://127.0.0.1:8080/api/v1/hosts | grep -q machine")
    machine.succeed("curl -sf http://127.0.0.1:8080/ | grep -q nixmox")
  '';
}
