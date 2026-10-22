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
joiner_pid=
cleanup() {
  if [[ -n $joiner_pid ]]; then kill "$joiner_pid" 2>/dev/null || true; wait "$joiner_pid" 2>/dev/null || true; fi
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
"$binary" --endpoint http://127.0.0.1:64310 cluster create physical-smoke --command-id cmd_physical_genesis --wait --json > "$work/genesis.json"
grep -q '"state": "succeeded"' "$work/genesis.json"
cat > "$work/joiner.yaml" <<EOF
schema_version: 1
data_dir: $work/joiner
http: {port: 64320}
machine: {port: 64321}
nats: {client_port: 64322, route_port: 64323, leaf_port: 64324}
network: {management_interfaces: [wlp3s0], preferred_interfaces: [wlp3s0]}
EOF
"$binary" serve --config "$work/joiner.yaml" > "$work/joiner.log" 2>&1 &
joiner_pid=$!
for attempt in $(seq 1 50); do
  if "$binary" --endpoint http://127.0.0.1:64320 identity show --json > "$work/joiner-original.json" 2>/dev/null; then break; fi
  kill -0 "$joiner_pid" || { cat "$work/joiner.log"; exit 1; }
  sleep 0.1
done
address=$(ip -4 -o address show dev wlp3s0 | awk '{split($4,a,"/"); print a[1]; exit}')
test -n "$address"
"$binary" --endpoint http://127.0.0.1:64320 cluster join --address "$address:64311" --name physical-smoke --command-id cmd_physical_join --json > "$work/join-receipt.json"
request_id=
for attempt in $(seq 1 50); do
  "$binary" --endpoint http://127.0.0.1:64310 cluster requests list --state awaiting_approval --json > "$work/requests.json"
  request_id=$(sed -n 's/^ *"id": "\(join_[a-z0-9]*\)",$/\1/p' "$work/requests.json")
  if [[ -n $request_id ]]; then break; fi
  sleep 0.1
done
[[ $request_id =~ ^join_[a-f0-9]{32}$ ]]
"$binary" --endpoint http://127.0.0.1:64310 cluster requests approve "$request_id" --version 1 --command-id cmd_physical_approve --wait --json > "$work/approved.json"
grep -q '"state": "succeeded"' "$work/approved.json"
timeout 45 "$binary" --endpoint http://127.0.0.1:64320 cluster join --address "$address:64311" --name physical-smoke --command-id cmd_physical_join --wait --json > "$work/joined.json"
grep -q '"state": "succeeded"' "$work/joined.json"
"$binary" --endpoint http://127.0.0.1:64320 cluster status --json > "$work/cluster.json"
grep -q '"broker_role": "leaf"' "$work/cluster.json"
grep -q '"membership": "pending"' "$work/cluster.json"
test ! -e "$work/joiner/pki/issuer/key.pem"
"$binary" --endpoint http://127.0.0.1:64320 identity show --json > "$work/joiner-identity.json"
node_id=$(sed -n 's/^ *"node_id": "\(n_[a-z0-9]*\)",$/\1/p' "$work/joiner-identity.json")
[[ $node_id =~ ^n_[a-f0-9]+$ ]]
timeout 45 "$binary" --endpoint http://127.0.0.1:64310 hosts refresh "$node_id" --command-id cmd_physical_refresh --wait --json > "$work/refreshed.json"
grep -q '"state": "succeeded"' "$work/refreshed.json"
grep -q '"name": "heliosmac"' "$work/refreshed.json"
grep -q '"environment": "bootstrap"' "$work/refreshed.json"
"$binary" --endpoint http://127.0.0.1:64310 hosts telemetry "$node_id" --json > "$work/telemetry.json"
grep -q '"freshness":"live"' "$work/telemetry.json"
grep -q '"interface":"wlp3s0"' "$work/telemetry.json"
systemctl is-active --quiet NetworkManager sshd
printf '%s\n' 'PASS: Wi-Fi inspection, private keys, crash recovery, deduplication, enrollment, remote host refresh and live telemetry'
REMOTE
