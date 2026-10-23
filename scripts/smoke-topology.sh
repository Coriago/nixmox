#!/usr/bin/env bash
# Three real daemons. Run inside the devshell; OTLP_ENDPOINT points at the fan-out collector.
set -euo pipefail
export KEEP_SMOKE=1
source "$(dirname "$0")/smoke-cluster.sh"

assert_replicas() {
  local replicas=$1 peers=$2
  cli 0 cluster status >"$work/status.json"
  jq -e --argjson replicas "$replicas" --argjson peers "$peers" '
    .quorum.available and .broker.metadata_peers==$peers and
    (.broker.streams|length)==4 and
    all(.broker.streams[]; .replicas==$replicas and .current==$replicas and .ready)
  ' "$work/status.json"
}
check_data() {
  cli 0 tasks show "$task" >"$work/preserved.json"
  jq -S '{id,command_id,owner_node_id,state,created_at,membership_epoch}' "$work/preserved.json" >"$work/current-data.json"
  cmp "$work/original-data.json" "$work/current-data.json"
}
jq -S '{id,command_id,owner_node_id,state,created_at,membership_epoch}' "$work/remote-task.json" >"$work/original-data.json"
cli 0 tasks logs "$task" | jq -S . >"$work/original-log.json"
assert_replicas 1 0

for n in 0 1 2; do
  timeout 120 "$work/nixmox" --endpoint "http://127.0.0.1:$base" --json hosts force-full-membership "${ids[n]}" --confirm-host "${ids[n]}" --wait >"$work/promote$n.json"
  jq -e '.state=="succeeded"' "$work/promote$n.json"
  check_data
done
assert_replicas 3 3
# Transfer the executor's logs over NATS to the authority while streams are R3.
# Node 2 owns this task; node 0 must fetch its bytes remotely.
cli 0 tasks logs "$task" | jq -S . >"$work/transferred-log.json"
cmp "$work/original-log.json" "$work/transferred-log.json"
cli 0 hosts refresh "${ids[2]}" --wait >"$work/r3-write.json"
jq -e '.state=="succeeded"' "$work/r3-write.json"
echo 'PASS: R3 current on all four streams; cross-host log transfer and durable task submission succeeded.'

for n in 2 1; do
  timeout 120 "$work/nixmox" --endpoint "http://127.0.0.1:$base" --json hosts demote "${ids[n]}" --confirm-host "${ids[n]}" --wait >"$work/demote$n.json"
  jq -e '.state=="succeeded"' "$work/demote$n.json"
  check_data
done
assert_replicas 1 0
# Make the original executor unavailable: the authority must still have its
# original terminal task projection, rather than hiding data loss via owner reads.
kill "${pids[2]}"
wait "${pids[2]}" || true
check_data
echo 'PASS: demotion returned to standalone R1 with the original data, including an offline-owner read.'
