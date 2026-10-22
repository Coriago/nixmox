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
          environment.systemPackages = [
            pkgs.curl
            pkgs.netcat-openbsd
          ];
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
        node.succeed("test $(stat -c %a /var/lib/nixmox/pki/broker/key.pem) = 600")
        node.fail("cmp /var/lib/nixmox/pki/daemon/key.pem /var/lib/nixmox/pki/broker/key.pem")
        node.wait_for_open_port(63201)
        node.wait_for_open_port(63205)
        machine_url = "https://" + identity["node_id"] + ":63205"
        tls = "curl --noproxy '*' --silent --cacert /var/lib/nixmox/pki/daemon/cert.pem --resolve " + identity["node_id"] + ":63205:127.0.0.1 -o /dev/null -w '%{http_code}' "
        assert node.succeed(tls + machine_url + "/api/v1/enrollment/requests/join_test") == "401"
        assert node.succeed(tls + machine_url + "/api/v1/commands") == "404"
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
    birch.fail("nc -z -w 1 alder 63201")
    for port in (63202, 63203, 63205):
        birch.succeed("nc -z -w 1 alder " + str(port))
  '';
}
