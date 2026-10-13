# Run by the root flake's github-e2e app, outside the Nix build sandbox so the
# guest has ordinary Internet access. Do not run against an existing host.
set -euo pipefail

work=$(mktemp -d "${TMPDIR:-/tmp}/nixmox-github-e2e.XXXXXX")
api=http://127.0.0.1:8081
vm_pid=
cleanup() {
  local result=$?
  if [[ -n "$vm_pid" ]]; then
    if [[ "${NIXMOX_E2E_KEEP_ALIVE:-0}" == 1 ]] && kill -0 "$vm_pid" 2>/dev/null; then
      echo "Test exit status: $result. VM retained for inspection at $api."
      echo "SSH: ssh -p 2221 root@localhost (password: nixos). Stop it with: kill $vm_pid"
      echo "E2E artifacts: $work"
      wait "$vm_pid" || true
    fi
    kill "$vm_pid" 2>/dev/null || true
    wait "$vm_pid" 2>/dev/null || true
  fi
  echo "E2E artifacts: $work"
  return "$result"
}
trap cleanup EXIT

# Port conflict must fail, rather than submit an apply to an unrelated service.
if curl --silent --max-time 2 "$api/healthz" >/dev/null; then
  echo 'Port 8081 already serves HTTP; stop that VM before running this test.' >&2
  exit 1
fi
cd "$work"
# A unique writable disk and QEMU PID belong exclusively to this invocation.
export NIX_DISK_IMAGE="$work/node1.qcow2"
"$NIXMOX_E2E_VM/bin/run-node1-vm" \
  -netdev user,id=e2ecluster,net=192.168.42.0/24,restrict=on \
  -device virtio-net-pci,netdev=e2ecluster,mac=52:54:00:12:34:01 >"$work/console.log" 2>&1 &
vm_pid=$!
echo "VM PID: $vm_pid; portal: $api; artifacts: $work"

request() { curl --fail-with-body --silent --show-error --max-time 30 "$@"; }
for ((i=0; i<180; i++)); do
  kill -0 "$vm_pid" 2>/dev/null || { tail -100 "$work/console.log"; exit 1; }
  if request "$api/healthz" >"$work/health.json" 2>/dev/null; then break; fi
  sleep 1
done
kill -0 "$vm_pid"
request "$api/healthz" | jq -e '.status == "ok"'
request "$api/api/v1/host" | jq -e '.hostname == "node1"'

wait_job() {
  local id=$1 state deadline=$((SECONDS + 3600))
  # Source builds may need downloads. Always retain the job response and logs.
  while ((SECONDS < deadline)); do
    kill -0 "$vm_pid" 2>/dev/null || { tail -100 "$work/console.log"; return 1; }
    if request "$api/api/v1/jobs/$id" >"$work/$id.json.tmp"; then
      mv "$work/$id.json.tmp" "$work/$id.json"
      state=$(jq -r '.state' "$work/$id.json")
      if [[ "$state" == applied || "$state" == failed ]]; then
        jq -r '.log[]?' "$work/$id.json" | tee "$work/$id.log"
        [[ "$state" == applied ]] || { echo "Failed job: $id" >&2; return 1; }
        return 0
      fi
    fi
    sleep 2
  done
  echo "Timed out waiting for $id" >&2
  return 1
}

command_job() {
  local name=$1 script=$2 id
  id=$(jq -nc --arg name "$name" --arg script "$script" \
    '{kind:"command",name:$name,command:"/run/current-system/sw/bin/bash",args:["-euc",$script]}' |
    request -X POST -H 'Content-Type: application/json' --data-binary @- "$api/api/v1/hosts/node1/jobs" | jq -er '.id')
  wait_job "$id"
}

command_job 'Verify bootstrap' 'test ! -e /etc/nixmox-e2e-generation; test ! -e /run/current-system/sw/bin/hello; readlink -f /run/current-system > /var/lib/nixmox/e2e-before; systemctl is-active nixmox'

# Pull from GitHub main, never a local fixture or a prebuilt target closure.
request "$api/api/v1/gitops" | tee "$work/gitops-before.json" |
  jq -e '.repo_url == "https://github.com/Coriago/nixmox.git" and .branch == "main"'
pull=$(request -X POST "$api/api/v1/gitops/pull" | jq -er '.id')
wait_job "$pull"
request "$api/api/v1/gitops" > "$work/gitops-pulled.json"
sha=$(jq -er '.last_pulled_sha | select(length == 40)' "$work/gitops-pulled.json")
printf 'GitHub main revision under test: %s\n' "$sha" | tee "$work/revision.txt"
command_job 'Verify pulled fixture' 'cd /var/lib/nixmox/git/repo; test -f e2e/flake.nix; test -f e2e/flake.lock; test -f e2e/configuration.nix; git rev-parse HEAD'

apply=$(request -X POST "$api/api/v1/hosts/node1/actions/apply" | jq -er '.id')
wait_job "$apply"
jq -e --arg sha "$sha" '.git_sha == $sha and .mode == "switch" and (.args | any(contains("dir=e2e")))' "$work/$apply.json"
# These substitutions deliberately execute in the guest shell.
# shellcheck disable=SC2016
command_job 'Verify activated GitHub configuration' 'test "$(cat /etc/nixmox-e2e-generation)" = github-main-e2e-v1; test "$(cat /var/lib/nixmox-e2e/activated)" = github-main-e2e-v1; test "$(/run/current-system/sw/bin/hello)" = "Hello, world!"; systemctl is-active nixmox-e2e-proof; systemctl is-active nixmox; test "$(readlink -f /run/current-system)" != "$(cat /var/lib/nixmox/e2e-before)"; test "$(readlink -f /run/current-system)" = "$(readlink -f /nix/var/nix/profiles/system)"'
request "$api/api/v1/hosts/node1/applylog" > "$work/applylog.json"
jq -e --arg id "$apply" --arg sha "$sha" 'any(.[]; .job_id == $id and .git_sha == $sha and .status == "success" and .gen_after > .gen_before)' "$work/applylog.json"
request "$api/" > "$work/portal.html"
request "$api/api/v1/jobs" > "$work/jobs.json"
echo 'PASS: GitHub main was pulled, built inside the VM, and switched; marker, package, activation unit, agent, logs, and history verified.'
