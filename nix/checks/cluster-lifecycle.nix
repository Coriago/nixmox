{ self, pkgs, ... }:
pkgs.testers.runNixOSTest {
  name = "cluster-lifecycle";
  nodes = builtins.listToAttrs (map (index: {
    name = "node${toString index}";
    value = { ... }: {
      imports = [ self.nixosModules.default ];
      services.nixmox = {
        enable = true;
        package = self.packages.${pkgs.stdenv.hostPlatform.system}.default;
        advertiseAddr = "192.168.1.${toString index}";
        gitops.pollIntervalSeconds = 0;
        allowConfigWrite = true;
      };
      networking.interfaces.eth1.ipv4.addresses = [ {
        address = "192.168.1.${toString index}";
        prefixLength = 24;
      } ];
      environment.systemPackages = [ pkgs.curl pkgs.jq ];
      virtualisation.memorySize = 768;
    };
  }) [ 1 2 3 ]);

  testScript = ''
    import json

    start_all()
    for node in [node1, node2, node3]:
        node.wait_for_unit("nixmox.service")
        node.wait_for_open_port(8080)

    node1.succeed("curl -sf -XPOST localhost:8080/api/v1/cluster/create -d name=lifecycle")
    for node in [node2, node3]:
        node.succeed("curl -sf -XPOST localhost:8080/api/v1/cluster/join/initiate -d seed=192.168.1.1:8080")

    node1.wait_until_succeeds(
        "curl -sf localhost:8080/api/v1/hosts | jq -e 'length == 3 and all(.online)'",
        timeout=90,
    )
    # Remote operations are dispatched to the selected host, not the coordinator.
    node1.succeed("curl -sf -XPUT localhost:8080/api/v1/hosts/node2/variables -H 'Content-Type: application/json' -d '{\"max_fan_speed\":45}'")
    node2.succeed("jq -e '.max_fan_speed == 45' /var/lib/nixmox/inputs.json")
    node1.fail("test -f /var/lib/nixmox/inputs.json")

    result = json.loads(node1.succeed(
        "curl -sf -XPOST localhost:8080/api/v1/hosts/node2/actions/leave -d confirm=node2"
    ))
    assert result["status"] == "unclustered", result
    assert not result["warnings"], result
    for node in [node1, node3]:
        node.wait_until_succeeds(
            "curl -sf localhost:8080/api/v1/hosts | jq -e 'length == 2 and all(.node_id != \"node2\")'",
            timeout=60,
        )
    node2.succeed("curl -sf localhost:8080/api/v1/bootstrap | jq -e '.status == \"unclustered\"'")
    node2.succeed("systemctl restart nixmox")
    node2.wait_for_open_port(8080)
    node2.succeed("curl -sf localhost:8080/api/v1/bootstrap | jq -e '.status == \"unclustered\"'")
    node2.succeed("curl -sf -XPOST localhost:8080/api/v1/cluster/join/initiate -d seed=192.168.1.1:8080")
    node1.wait_until_succeeds(
        "curl -sf localhost:8080/api/v1/hosts | jq -e 'length == 3 and all(.online)'",
        timeout=90,
    )
  '';
}
