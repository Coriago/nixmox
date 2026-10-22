{ pkgs, module }:
pkgs.testers.runNixOSTest {
  name = "nixmox-bootstrap";
  nodes = builtins.listToAttrs (
    map
      (name: {
        inherit name;
        value = { ... }: {
          imports = [ module ];
          services.nixmox = {
            enable = true;
            http.port = 63200;
            machine.port = 63205;
            nats = {
              clientPort = 63201;
              routePort = 63202;
              leafPort = 63203;
            };
            discovery.port = 63204;
            network.managementInterfaces = [ "eth1" ];
          };
          virtualisation.memorySize = 1024;
          virtualisation.diskSize = 2048;
        };
      })
      [
        "alder"
        "birch"
        "cedar"
      ]
  );
  testScript = ''
    import json
    start_all()
    identities = {}
    for node in (alder, birch, cedar):
        node.wait_for_unit("nixmox.service")
        node.wait_for_open_port(63200)
        identity = json.loads(node.succeed("nixmox identity show --json"))
        identities[node.name] = identity
        task = json.loads(node.succeed("nixmox network check --command-id cmd_vm_check --wait --json"))
        assert task["state"] == "succeeded", task
        assert task["result"]["network"]["status"] == "ready", task
        assert task["result"]["network"]["interface"] == "eth1", task
        node.succeed("test $(stat -c %a /var/lib/nixmox/pki/daemon/key.pem) = 600")
        node.succeed("systemctl kill --signal=KILL nixmox.service")
        node.wait_for_unit("nixmox.service")
        node.wait_until_succeeds("nixmox identity show --json")
        assert json.loads(node.succeed("nixmox identity show --json"))["node_id"] == identity["node_id"]
        retried = json.loads(node.succeed("nixmox network check --command-id cmd_vm_check --wait --json"))
        assert retried["id"] == task["id"]
        assert retried["version"] == task["version"]
    assert len(set(v["node_id"] for v in identities.values())) == 3
    remote = json.loads(birch.succeed("nixmox --endpoint http://alder:63200 tasks list --json"))
    assert remote["items"][0]["owner_node_id"] == identities["alder"]["node_id"]
  '';
}
