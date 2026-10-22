#!/usr/bin/env bash
set -euo pipefail
# Run with nix develop -c bash tests/physical/smoke.sh /nix/store/...-nixmox-... .
# The closure must already be copied to the authorized host. No boot or disk effects.
binary=${1:?absolute Nix store package path required}/bin/nixmox
[[ $binary =~ ^/nix/store/[a-z0-9]{32}-nixmox-[a-zA-Z0-9.+_-]+/bin/nixmox$ ]]
ssh -o BatchMode=yes -o ConnectTimeout=10 -o StrictHostKeyChecking=yes root@heliosmac.local bash -s -- "$binary" <<'REMOTE'
set -euo pipefail
test "$(hostname)" = heliosmac
test "$(uname -m)" = x86_64
binary=$1
test -x "$binary"
work=$(mktemp -d /run/nixmox-smoke.XXXXXXXX)
daemon_pid=
cleanup() {
  if [[ -n $daemon_pid ]]; then kill "$daemon_pid" 2>/dev/null || true; wait "$daemon_pid" 2>/dev/null || true; fi
  rm -rf -- "$work"
}
trap cleanup EXIT
umask 077
export XDG_STATE_HOME="$work/client"
cat > "$work/config.yaml" <<EOF
schema_version: 1
data_dir: $work/state
http: {port: 64310}
machine: {port: 64311}
nats: {client_port: 64312, route_port: 64313, leaf_port: 64314}
network: {management_interfaces: [wlp3s0], preferred_interfaces: [wlp3s0]}
EOF
start() {
  "$binary" serve --config "$work/config.yaml" > "$work/daemon.log" 2>&1 &
  daemon_pid=$!
  for attempt in $(seq 1 50); do
    if "$binary" --endpoint http://127.0.0.1:64310 identity show --json > "$work/current.json" 2>/dev/null; then return; fi
    kill -0 "$daemon_pid" || { cat "$work/daemon.log"; return 1; }
    sleep 0.1
  done
  cat "$work/daemon.log"
  return 1
}
start
cp "$work/current.json" "$work/original.json"
"$binary" --endpoint http://127.0.0.1:64310 network check --command-id cmd_physical_smoke --wait --json > "$work/task.json"
grep -q '"state": "succeeded"' "$work/task.json"
grep -q '"interface": "wlp3s0"' "$work/task.json"
grep -q '"status": "ready"' "$work/task.json"
test "$(stat -c %a "$work/state/pki/daemon/key.pem")" = 600
test "$(stat -c %a "$work/state/pki/broker/key.pem")" = 600
! cmp -s "$work/state/pki/daemon/key.pem" "$work/state/pki/broker/key.pem"
kill -KILL "$daemon_pid"
wait "$daemon_pid" 2>/dev/null || true
daemon_pid=
start
cmp "$work/original.json" "$work/current.json"
"$binary" --endpoint http://127.0.0.1:64310 network check --command-id cmd_physical_smoke --wait --json > "$work/retried.json"
cmp "$work/task.json" "$work/retried.json"
systemctl is-active --quiet NetworkManager sshd
printf '%s\n' 'PASS: physical Wi-Fi inspection, private distinct machine keys, crash recovery and command deduplication'
REMOTE
