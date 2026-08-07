{ pkgs, self }:

pkgs.writeShellApplication {
  name = "nixmox-demo";
  runtimeInputs = [ pkgs.coreutils pkgs.qemu ];
  text = ''
    set -euo pipefail

    VLAN_PORT=14242
    WORK="''${NIXMOX_DEMO_DIR:-/tmp/nixmox-demo}"
    mkdir -p "$WORK"
    cd "$WORK"

    PIDS=()
    cleanup() {
      echo "Stopping VMs..."
      for pid in "''${PIDS[@]:-}"; do
        [ -n "$pid" ] && kill "$pid" 2>/dev/null || true
      done
      wait 2>/dev/null || true
    }
    trap cleanup EXIT

    for i in 1 2 3; do
      name="node$i"
      if [ ! -x "$WORK/$name/bin/run-$name-vm" ]; then
        echo "Building $name VM (first build takes a while)..."
        nix build "${self}#nixosConfigurations.$name.config.system.build.vm" -o "$WORK/$name"
      fi
    done

    for i in 1 2 3; do
      name="node$i"
      mac=$(printf "52:54:00:12:34:%02x" "$i")
      echo "Starting $name (portal: http://localhost:808$i, ssh: -p 222$i root@localhost)..."
      "$WORK/$name/bin/run-$name-vm" \
        -netdev socket,id=vlan0,mcast=230.0.0.1:$VLAN_PORT,localaddr=127.0.0.1 \
        -device virtio-net-pci,netdev=vlan0,mac="$mac" \
        > "$WORK/$name.log" 2>&1 &
      PIDS+=("$!")
      sleep 2
    done

    cat <<'BANNER'

=== nixmox demo cluster ===
Portals:  http://localhost:8081  http://localhost:8082  http://localhost:8083
SSH:      ssh -p 2221 root@localhost  (pw: nixos)   [2222/2223 for node2/node3]
Cluster:  192.168.42.1 / .2 / .3  (multicast LAN, NATS on :6222)

MVP click-path:
  1. Open :8081 -> "Create new cluster", name it (e.g. homelab)
  2. Open :8082 -> "Join existing cluster", seed 192.168.42.1:8080
  3. Same for :8083; all three portals should list 3 online hosts
  4. :8081 -> GitOps page -> set the repo URL -> Pull now -> Apply

Ctrl-C stops all VMs. Logs: /tmp/nixmox-demo/node*.log
BANNER

    wait
  '';
}
