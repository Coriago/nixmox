#!/usr/bin/env bash
# Temporary local smoke check: nix develop -c bash scripts/smoke-cluster.sh
# Three real daemons on one OS, no VMs. Dynamic samples need not be identical.
set -euo pipefail
cd "$(dirname "$0")/.."
work=$(mktemp -d /tmp/nixmox-smoke.XXXXXX)
base=${BASE_PORT:-64270}
pids=() ids=() jobs=()
cleanup() {
  status=$?
  for pid in "${pids[@]}"; do kill "$pid" 2>/dev/null || true; done
  wait || true
  if ((status)); then
    echo "Smoke check failed; logs/state retained in $work" >&2
    for log in "$work"/*.log; do [[ ! -f $log ]] || tail -n 20 "$log" >&2; done
  elif [[ ${KEEP_SMOKE:-0} == 1 ]]; then
    echo "Logs/state retained in $work"
  else
    rm -rf -- "$work"
  fi
}
trap cleanup EXIT
[[ $base =~ ^[0-9]+$ ]] && ((base > 1024 && base <= 65512))
go build -o "$work/nixmox" ./cmd/nixmox
cli() { local node=$1; shift; "$work/nixmox" --endpoint "http://127.0.0.1:$((base+node*10))" --json "$@"; }

for n in 0 1 2; do
  port=$((base+n*10))
  cat >"$work/$n.yaml" <<EOF
data_dir: $work/node$n
node_name: smoke-$n
http:
  port: $port
nats:
  client_port: $((port+1))
  route_port: $((port+2))
  leaf_port: $((port+3))
network:
  management_interfaces: []
EOF
  if [[ -n ${OTLP_ENDPOINT:-} ]]; then
    printf 'telemetry:\n  otlp:\n    enable: true\n    endpoint: %s\n    metrics_interval_ms: 1000\n' "$OTLP_ENDPOINT" >>"$work/$n.yaml"
  fi
  "$work/nixmox" --config "$work/$n.yaml" serve >"$work/$n.log" 2>&1 &
  pids+=("$!")
  for attempt in {1..50}; do
    kill -0 "${pids[n]}" || exit 1
    if cli "$n" identity show >"$work/identity$n.json" 2>/dev/null &&
      [[ -f $work/node$n/identity.json ]] &&
      [[ $(jq -r .node_id "$work/identity$n.json") == $(jq -r .node_id "$work/node$n/identity.json") ]]; then break; fi
    sleep .1
  done
  # Never mutate an unrelated daemon if a requested port was already occupied.
  ids[n]=$(jq -r .node_id "$work/node$n/identity.json")
  [[ ${ids[n]} == $(jq -r .node_id "$work/identity$n.json") ]]
done

cli 0 cluster create smoke --wait | jq -e '.state == "succeeded"'
for n in 1 2; do
  jobs[n]=$(cli "$n" cluster join --address "127.0.0.1:$base" --name smoke --detach | jq -r .task_id)
done
for attempt in {1..50}; do
  cli 0 cluster requests list >"$work/requests.json"
  if jq -e '.items|length==2' "$work/requests.json" >/dev/null; then break; fi
  sleep .2
done
jq -e '.items|length==2' "$work/requests.json" >/dev/null
while read -r request version; do
  cli 0 cluster requests approve "$request" --version "$version" --wait | jq -e '.state == "succeeded"'
done < <(jq -r '.items[] | [.request_id,.version] | @tsv' "$work/requests.json")

for n in 1 2; do
  for attempt in {1..30}; do
    cli "$n" tasks show "${jobs[n]}" >"$work/task$n.json"
    if jq -e '.state=="succeeded"' "$work/task$n.json" >/dev/null; then break; fi
    sleep .1
  done
  jq -e '.state=="succeeded"' "$work/task$n.json"
done

for n in 0 1 2; do
  for attempt in {1..100}; do
    cli "$(((n+1)%3))" hosts telemetry "${ids[n]}" >"$work/sample$n.json"
    if jq -e '.freshness=="live" and .cpu.logical_count!=null and .memory.total_bytes>0' "$work/sample$n.json" >/dev/null; then break; fi
    sleep .2
  done
  jq -e '.freshness=="live" and .cpu.logical_count!=null and .memory.total_bytes>0' "$work/sample$n.json" >/dev/null
  jq '{host_id,membership_epoch,sequence,freshness,cpu,memory}' "$work/sample$n.json"

done
# Same metric fields and physical-host capacities; utilization, counters and
# timestamps naturally move between independently scheduled observations.
jq -es 'map({fields:keys,cpu:.cpu.logical_count,memory:.memory.total_bytes}) | unique | length==1' "$work"/sample*.json
cli 0 hosts list | jq -e '(.items|length)==3 and all(.items[];.membership=="pending" and .reachability=="online")'
for gateway in 0 1 2; do
  cli "$gateway" hosts list | jq -e '(.items|length)==3'
  cli "$gateway" cluster status | jq -e '.quorum.available and .quorum.replicas==1'
done
cli 0 hosts refresh "${ids[2]}" --wait >"$work/remote-task.json"
jq -e --arg owner "${ids[2]}" '.state=="succeeded" and .scope=="cluster" and .owner_node_id==$owner' "$work/remote-task.json"
task=$(jq -r .id "$work/remote-task.json")
cli 0 tasks logs "$task" | jq -e '.next_offset>0'
cli 2 tasks show "$task" | jq -e '.state=="succeeded"'
printf 'PASS: three gateways, shared inventory/metrics, remote task and owner logs. One durable replica (not HA).\n'
