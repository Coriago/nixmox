{ self, pkgs }:

pkgs.testers.runNixOSTest {
  name = "jobs-runner";

  nodes.machine = { config, pkgs, ... }: {
    imports = [
      self.nixosModules.default
    ];

    environment.systemPackages = [ pkgs.jq ];

    services.nixmox = {
      enable = true;
      package = self.packages.${pkgs.stdenv.hostPlatform.system}.default;
    };
  };

  testScript = ''
    machine.wait_for_unit("nixmox.service")
    machine.wait_for_open_port(8080)

    machine.succeed("curl -sf -XPOST http://127.0.0.1:8080/api/v1/jobs -d '{\"kind\":\"echo\",\"args\":[\"hi\"]}' -H 'Content-Type: application/json' | grep -q '\"id\"'")
    
    # Print the log of the job
    machine.succeed("ID=$(curl -sf http://127.0.0.1:8080/api/v1/jobs | jq -r '.[0].id') && curl -sf http://127.0.0.1:8080/api/v1/jobs/$ID >&2")
    
    machine.wait_until_succeeds("curl -sf http://127.0.0.1:8080/api/v1/jobs | grep -q applied")
  '';
}
