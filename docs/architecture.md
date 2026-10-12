# Architecture and compatibility

## Boundaries

- `cmd/nixmox`: CLI/config resolution and signal handling, no subsystem construction.
- `daemon`: composition root; pairs startup with cleanup on success and error.
- `config`: typed runtime settings, address resolution and validation.
- `hostinfo`: read-only Linux inventory. Shared two-second telemetry sampling reads procfs/sysfs without spawning subprocesses.
- `jobs`: persisted tasks, per-host execution exclusion, command/log handling. `Runner.Submit` centralizes IDs, timestamps and rejected-task terminalization. Command kinds are allowlisted; commands receive argument vectors, not interpolated shells.
- `apply` / `gitops`: Git synchronization, previews, revision-pinned builds and apply records. Local host inputs are intentionally impure.
- `cluster` / `natsx`: serialized membership transitions, generation-scoped heartbeats/watchers, local-first record mirroring and the embedded server. Removing a node archives old cluster data rather than reusing its Raft identity.
- `discovery`: bounded, expiring multicast hints; never admission or authorization.
- `server`: transport, request validation, peer routing and rollout coordination. Forwarded operations resolve only registered hosts and have a one-hop loop guard.
- `portal`: one renderer and shared page data. Full pages and SSE fragments use the same templates. Static assets cannot expose template source.
- `statefile`, `logbuf`, `power`: small reusable primitives for atomic private state, bounded log tails and WoL packets.

## State

Existing `node.json`, `git/state.json`, `jobs/` and local `applylog/` formats remain readable. New JSON fields are additive. Old nodes without advertised HTTP addresses cannot receive remote operations: upgrade them first.

Additional state:

- `hosts.json`: last-known inventory; loaded offline until a heartbeat arrives.
- `inputs.json`: local scalar configuration values (0600).
- `nats-clustered.left-*`: archived cluster state after leaving; retain until membership and local histories are verified.
- `node.json.left`: records intentional departure, suppressing first-boot autojoin.

Do not reuse a hostname for two machines. The seed rejects a join with its own hostname; operators must also ensure uniqueness across all peers. Reachability is not identity verification.

## Operational limitations

Authentication/TLS, partition-safe forced eviction, distributed rollout locks, durable rollout resumption, application health gates and an evaluated NixOS activation diff are not implemented. Do not infer these guarantees from a successful HTTP response. Rollout coordination and daemon-log tails are in-memory.

System command success means the command finished successfully. A scheduled shutdown is not observed power-off, and sending WoL is not observed power-on. Provisioning assumes preconfigured SSH access and a reviewed target flake; it does not infer or validate which physical disk disko should erase.

Cluster hosts and record mirrors are cached views. Each node's local job and apply history remains important; snapshot the data directories before topology changes. Removing unreachable members and repairing partitioned clusters require operator intervention.

## Verification

Unit/integration coverage includes request safety, remote targeting, SSE escaping/disconnect, variable validation/persistence, bounded log tails, telemetry parsing, WoL encoding, rejected jobs, state-file replacement, and embedded two-node leave/rejoin. The existing NixOS VM checks cover portal, jobs, Git synchronization and rebuild/rollback.

The former two-node test incorrectly required the seed to be elected leader: the NATS peer-list API is leader-only. It now accepts either node as leader. The apply VM fixture also needed its Python indentation corrected and fail-fast task-log assertions. It now selects prebuilt real NixOS generation closures, permitting actual rebuild/boot/rollback in an offline VM without source downloads. This exposed a rollback re-execution bug: rollback now uses the installed rebuild binary instead of evaluating an unrelated `/etc/nixos` configuration.

A three-node NixOS VM check verifies remote input writes, cluster removal without warnings, persistence across restart, and rejoining. Removing routes uses live NATS reloads rather than restarting surviving Raft members.

Physical Wake-on-LAN, destructive installation, and board-specific image booting must be validated on disposable hardware. Image evaluation is not a boot test.
