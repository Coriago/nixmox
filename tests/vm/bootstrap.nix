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
    import json, time
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
    genesis = json.loads(alder.succeed("nixmox cluster create homelab --command-id cmd_vm_genesis --wait --json"))
    assert genesis["state"] == "succeeded", genesis
    cluster = json.loads(alder.succeed("nixmox cluster status --json"))
    assert cluster["membership"] == "pending" and cluster["full_members"] == 0 and cluster["quorum"]
    alder.succeed("systemctl kill --signal=KILL nixmox.service")
    alder.wait_for_unit("nixmox.service")
    alder.wait_until_succeeds("nixmox cluster status --json")
    recovered = json.loads(alder.succeed("nixmox cluster status --json"))
    assert recovered["cluster_id"] == cluster["cluster_id"]
    replayed = json.loads(alder.succeed("nixmox cluster create homelab --command-id cmd_vm_genesis --wait --json"))
    assert replayed["id"] == genesis["id"]
    assert alder.succeed("curl --noproxy '*' --silent --cacert /var/lib/nixmox/trust/ca.pem --resolve daemon.nixmox:63205:127.0.0.1 -o /dev/null -w '%{http_code}' https://daemon.nixmox:63205/api/v1/enrollment/requests/join_test") == "401"
    for node in (birch, cedar):
        command = "nixmox cluster join --address alder:63205 --name homelab --command-id cmd_vm_join --json"
        receipt = json.loads(node.succeed(command))
        deadline = time.monotonic() + 30
        request = None
        while time.monotonic() < deadline:
            requests = json.loads(alder.succeed("nixmox cluster requests list --state awaiting_approval --json"))
            request = next((r for r in requests["items"] if r["node_id"] == identities[node.name]["node_id"]), None)
            if request:
                break
            time.sleep(0.2)
        assert request, "Join request did not arrive"
        assert json.loads(node.succeed("nixmox identity show --json"))["cluster_id"] is None
        node.succeed("systemctl kill --signal=KILL nixmox.service")
        node.wait_for_unit("nixmox.service")
        node.wait_until_succeeds("nixmox identity show --json")
        assert json.loads(node.succeed(command))["task_id"] == receipt["task_id"]
        approval = "nixmox cluster requests approve " + request["id"] + " --version " + str(request["version"]) + " --command-id cmd_approve_" + node.name + " --wait --json"
        approved = json.loads(alder.succeed(approval))
        assert approved["state"] == "succeeded", approved
        assert json.loads(alder.succeed(approval))["id"] == approved["id"]
        joined_task = json.loads(node.succeed(command + " --wait"))
        assert joined_task["state"] == "succeeded", joined_task
        assert joined_task["result"]["membership"] == "pending"
        node.wait_until_succeeds("nixmox cluster status --json")
        status = json.loads(node.succeed("nixmox cluster status --json"))
        assert status["cluster_id"] == cluster["cluster_id"] and status["broker_role"] == "leaf" and status["full_members"] == 0
        node.fail("test -e /var/lib/nixmox/pki/issuer/key.pem")
        node.succeed("systemctl kill --signal=KILL nixmox.service")
        node.wait_for_unit("nixmox.service")
        node.wait_until_succeeds("nixmox cluster status --json")
        current = json.loads(node.succeed("nixmox identity show --json"))
        assert current["node_id"] == identities[node.name]["node_id"] and current["key_id"] == identities[node.name]["key_id"]
        assert json.loads(node.succeed(command + " --wait"))["id"] == joined_task["id"]
    alder.succeed("systemctl stop nixmox.service")
    birch.fail("nixmox cluster status --json")
    assert json.loads(birch.succeed("nixmox network check --wait --json"))["state"] == "succeeded"
    alder.succeed("systemctl start nixmox.service")
    birch.wait_until_succeeds("nixmox cluster status --json")
    cedar.wait_until_succeeds("nixmox cluster status --json")
  '';
}
