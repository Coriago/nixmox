# Three-node cluster E2E

This is the project's GitHub E2E suite. It replaces the previous single-VM
`run.sh`; there is one entry point, `nix run .#github-e2e`. Lower-level offline
NixOS checks under `nix/checks/` remain regression tests, not a second E2E suite.

The suite uses three fresh disk-booted NixOS VMs, the public nixmox HTTP APIs,
real LAN discovery, and an explicitly recorded Git source: GitHub `main` by
default, or an immutable working-tree snapshot when requested. No prebuilt target closure,
mock rebuild, seeded membership, or direct edits to cluster state are used.
The harness uses SSH and QEMU QMP for observation and VM power control only.

## Run and interact

For GitHub mode, publish the accompanying application, VM configuration, and
fixture changes through the normal authorized workflow before the deployment
run. Do not push to `main` merely to satisfy a test prerequisite. The `e2e/flake.nix`
relative `nixmox` input uses the module and Go source from that same Git commit.
Its lock file pins nixpkgs. All three guests pull independently, and the suite
fails if `main` moved and they obtained different revisions. The exact revision
is recorded in `run.json`; it is not silently replaced with local working files.

The comprehensive run of **both parts** is the main final acceptance test for
application changes. Extend this harness for changed workflows; lower-level VM,
unit and browser tests complement it rather than replace it.

For unpublished work, use `--source-tree`. The harness copies tracked and
non-ignored untracked files into a new Git repository in the artifact directory,
creates a snapshot commit, and serves it read-only on a loopback Git daemon.
Guests independently clone it through QEMU's host address and perform real Nix
source builds and activation. The only generated source file is
`e2e/source.json`, selecting that run's Git URL in the deployed fixture so daemon
restarts keep the same origin. No application files are rewritten, no target
closures are prebuilt, and nothing is committed or pushed in the user's repo.
The manifest records the base revision, dirty status, snapshot SHA and source
mode; `source/` preserves every included file and `working-tree.patch` preserves
the tracked diff. This mode exercises the full cluster lifecycle but is not a
GitHub-network/credentials test. The Git server is retained with retained VMs
and stopped by `--stop`/normal cleanup.

```sh
# Full acceptance of the current working tree, including new untracked files:
nix run path:.#github-e2e -- --source-tree "$PWD"

# Both parts, followed by VM cleanup:
nix run .#github-e2e

# Happy path only. Return to your shell with the healthy cluster running:
nix run .#github-e2e -- --part 1

# After your interactive work, continue with teardown on those same disks:
nix run .#github-e2e -- --part 2 --work-dir /tmp/nixmox-cluster-e2e.EXAMPLE

# Retain the VMs on failure or after the full suite:
nix run .#github-e2e -- --keep-alive

# Stop VMs owned by a particular run without performing cluster teardown:
nix run .#github-e2e -- --stop --work-dir /tmp/nixmox-cluster-e2e.EXAMPLE
```

`NIXMOX_E2E_KEEP_ALIVE=1` also enables retention. Part 1 always retains VMs after
success. Part 2 first checks that the same three-node cluster is healthy; if
manual experiments changed membership or left jobs running, restore the expected
state or start a fresh run. A failed phase never advances the checkpoint. Part 2
is destructive to this disposable cluster; it is never started automatically by
`--part 1`.

The runner prints the artifact directory and commands to resume or stop. Portals
are <http://localhost:8081>, <http://localhost:8082>, and
<http://localhost:8083>. SSH ports are 2221, 2222, and 2223, with user `root` and
password `nixos`. Job pages show streaming logs, including during source builds.
Keep the runner in a terminal pane for named stages and command output.

Each invocation owns its disks, QMP sockets, multicast network port, and console
logs. Ports 8081–8083 and 2221–2223 must be free; an existing listener causes a
failure before any request is sent to it. The suite never operates on the
physical host's nixmox instance on port 8080. Do not use these deliberately
insecure VM configurations on a physical machine or an untrusted network.

## Part 1: happy path and interactive checkpoint

| Step | Operation | Required evidence |
| --- | --- | --- |
| 1.1 | Boot three standalone VMs and wait for bidirectional discovery. | Each node has sufficient writable build space, is unclustered, and has standalone JetStream and R=1 streams; each discovers both peers on the cluster LAN. Discovery itself does not change membership. |
| 1.2a | Create `github-e2e` on node1. | A named one-node cluster remains standalone, without metadata Raft. |
| 1.2b | Node2 joins using node1's HTTP peer address. | Both nodes agree on two members, metadata Raft is active with a leader and two members, and streams remain R=1. |
| 1.2c | Node1 explicitly adds discovered node3 through `/api/v1/cluster/add`. | Node3 was unjoined until the add; all nodes converge on three members. Metadata Raft has three members; both `hosts` and `applylog` streams reach R=3 with current replicas. A write sent through each node receives a commit ACK. |
| 1.3 | Pull this repository's `main` branch on every guest. | Real Git pull jobs succeed and all three revision SHAs match. The fixture and application are built from that revision. |
| 1.3a | Request a system preview through node2 for node1. | The candidate is pinned to the pulled SHA, includes GNU `hello`, marker and proof-service changes, and leaves the running system and apply history untouched. Check remote preview HTML, scoped/global tasks, host task subscriptions, and default media-write denial. |
| 1.4 | Submit an apply through node2 targeting node1. | Only node1 switches, using `e2e#node1`. The distinct marker, GNU `hello`, activation proof service, and changed active system profile prove activation. Node2 and node3 still lack the additions. Node1’s live sync fragment changes to `Revision applied`. |
| 1.5 | Start a serial rollout through node1. | The rollout completes on all three nodes, with the same revision and each node's own flake attribute. Every node has deployment proof and a successful apply record; restarting each daemon verifies the installed executable and retained job logs. Existing rollout semantics reapply node1 as well as deploying node2 and node3. |
| 1.6 | Gracefully power off the original seed, node1. | The surviving pair retains node1 as an offline member, continues committing cluster writes, and executes node2-to-node3 tasks. Output is observable while a task runs; a second task queues behind it; stderr and a nonzero exit code persist; remote uptime advances. SSE fragments contain a running progress bar and live output, global and host task scopes agree, complete-log download works through another node, and offline-host warnings remain visible. |
| 1.7 | Boot node1 from the same disk. | A changed boot ID proves an actual restart. The applied generation boots, node1 reconnects without another join, all three inventories agree, replicas catch up, and writes commit again. |
| Checkpoint | Save `part1-passed`. | All three deployed VMs remain available when `--part 1` was selected. |

The fixture adds `/etc/nixmox-e2e-generation` (`github-main-e2e-v1`), GNU `hello`,
and `nixmox-e2e-proof.service`, whose output is in `/var/lib/nixmox-e2e/`.
These additions are absent from the bootstrap configuration. Both bootstrap and
target use a partitioned boot disk and GRUB, so restart tests boot the selected
generation instead of forcing the bootstrap kernel through QEMU.

## Part 2: quorum failure, teardown, and reuse

Offline members and removed members must have different effects. Loss of two
members in a three-member group must **not** automatically shrink it to one.
That would permit conflicting independent clusters during a network partition.
Graceful removal, performed while quorum is available, deliberately changes the
membership and permits de-escalation.

| Step | Operation | Required evidence |
| --- | --- | --- |
| 2.1 | Revalidate Part 1's checkpoint. | Three current metadata members, all three streams at R=3 with current replicas, and an acknowledged cluster write. This avoids testing a cluster that never finished escalation. |
| 2.2 | Abruptly terminate node2 and node3's QEMU processes, preserving their disks. | Node1 retains a three-member identity and R=3 stream configuration. Writes to the existing replicated hosts stream cannot receive a successful commit ACK. Repeated checks run for at least 75 seconds, longer than the replica scaler's interval. The HTTP API and NATS transport remain responsive; the node does not silently become standalone. |
| 2.3 | Restart node2, then node3. | Restoring one peer regains quorum and acknowledged writes; node1-to-node2 tasks and changing heartbeat information work while node3 remains offline. The third peer catches up and the three-member R=3 topology returns. |
| 2.4a | Gracefully remove node3 through node1. | All surviving views agree on two members; all three streams de-escalate to R=1 before quorum shrinks. Two-member metadata Raft remains active. The removed node is standalone and stays unclustered after a daemon restart. |
| 2.4b | Gracefully remove node2. | Node1 retains the cluster name but runs standalone JetStream: no metadata Raft, R=1 streams, and successful writes. Node2 stays removed across restart. |
| 2.5 | Rejoin node2, execute cross-node tasks, then remove it again. | Metadata Raft escalates from standalone to two members again; operational tests pass; removal returns node1 to standalone again. |
| 2.6 | Leave/delete the named cluster on the last node. Create a new, distinct cluster on each detached node, then delete those too. | Old membership and names do not return across daemon restarts. Every former member can create a fresh cluster, commit metadata, and execute a local task. Final state: all three nodes unclustered. |

“Frozen” means **replicated cluster writes cannot commit**, not that the whole
service should hang. Existing local logs and HTTP health remain readable without Raft quorum.
New local commands use the same replicated command stream as remote commands
and therefore cannot bypass quorum loss. A timed-out publish may commit after quorum returns;
the test does not mistake timeout for a guarantee that the write was discarded.

The write probe uses a unique `e2e.<nonce>` key in the existing `hosts` stream,
outside the `hosts.>` membership watcher. It expires with that stream's TTL.
Using the existing R=3 stream matters: successfully writing a separate R=1 test
bucket would not establish cluster availability. SSH forwards the loopback NATS
connection; NATS is not exposed on the LAN. Protocol PING/PONG before and after
the write distinguishes loss of quorum from a broken connection.

`GET /api/v1/cluster/diagnostics` reports the local embedded server's metadata
Raft and stream replica state, even without quorum. It is a snapshot, not a
write-health guarantee; the commit probes supply that evidence. Assertions check
both the metadata Raft group and the stream replication factor. R=1 streams on a
two-member metadata group do **not** mean metadata Raft has been disabled.

## Automation, artifacts, and scope

The Python runner uses bounded polling instead of fixed startup
sleeps. Source-build jobs allow up to an hour each; a three-node rollout allows
three hours. Transition checks have shorter explicit deadlines. Only the quorum
stability window and the task streaming probe intentionally hold time open.

`run.json` records the stage, phase checkpoint, revision, VM executables, and PIDs.
`runner.log`, per-node serial consoles, job JSON/log output, rollout status,
quorum probe responses, topology snapshots, and service journals remain on disk
after cleanup. Failure captures snapshots before VM cleanup. QMP control sockets
belong to the run, so cleanup does not kill arbitrary processes using stale PIDs.
Retained runs need their referenced Nix store paths to remain available.

Budget for three 4-GiB VMs, six virtual CPUs, and three sparse 16-GiB disks, plus
host build/cache space. Guests have Internet access for Git and Nix substitutes;
their shared cluster LAN is separate. Each guest starts with only its bootstrap
store closure and a persistent writable store overlay. The deployed source is
built in the guests; normal binary cache substitution is allowed.

This suite covers process/VM loss and persistent-disk restart. It does not yet
claim asymmetric network partition, packet loss, disk corruption, authentication,
or physical USB boot coverage. Adding such fault modes should preserve the same
quorum invariants rather than weaken them. The suite drives the same HTTP APIs and SSE-rendered fragments
used by the UI; the cluster-add button is available in Operations discovery.

The host-navigation regression in step 1.4 opens the hosts/sidebar SSE stream
before submitting the remote apply, then requires a subsequent event containing
the deployment task, spinner and progress. It checks all sidebar members and the
single correct serving-node marker, then verifies Reapply versus Apply update
and removal of the deployment indicator after success.

The command-transport regression in step 1.2d exercises both local and remote
submission with an `Idempotency-Key`, verifies the same job/receipt is returned
on retry, restarts the destination daemon, and verifies a further retry does not
repeat the command's external effect. For the remote case it blocks the
observer's HTTP connection to the target; command submission, task reads and
receipt retrieval must still succeed through NATS. All topology checks now
include the durable `nixmox_commands` stream alongside the two KV streams.

Step 1.4b runs Chromium/Playwright against the live VM cluster. It checks sidebar
membership/serving-node identity, steady one-second uptime during bursty events
and quiet periods, offline/reboot clocks, connection loss and reconnect, alternate
host links, two-second activity versus 30-second inventory, incremental remote
log streaming with Unicode/HTML-safe output and reconnect catch-up, and mobile overflow. The Nix launcher
supplies the browser and Python dependencies. Browser screenshots and timestamped
uptime/telemetry observations are retained with the run artifacts.
