# Host inventory and lifecycle

Every node exposes a console. Joining, configuration readiness, and reachability
are independent state axes: unreachable does not mean removed, and joined does
not mean fully managed.

## Host record

```json
{
  "schema_version": 1,
  "id": "n_cedar",
  "version": 7,
  "name": "cedar",
  "cluster_id": "c_lab",
  "membership": "pending",
  "environment": "installer",
  "reachability": "online",
  "health": {"status": "unknown", "observed_at": null, "reasons": []},
  "last_seen": "2026-10-20T12:00:00Z",
  "configuration": {"repository_id": "r_lab", "name": "cedar", "ref_id": "ref_cedar"},
  "deployment_verified": false,
  "pending_reasons": ["configuration_not_deployed"],
  "endpoints": [
    {"service": "api", "transport": "http", "address": "192.168.1.32:63170", "interface": "enp1s0", "priority": 20, "verified": true}
  ],
  "capabilities": {"build": true, "install": true, "media_write": false},
  "broker_role": "leaf",
  "active_task_ids": []
}
```

`configuration` and `cluster_id` may be null. Membership is `unjoined`, `pending`,
or `full`; environment is `bootstrap`, `installer`, or `installed`. `bootstrap`
means a temporary agent on an existing OS; `installed` means the permanent managed
NixOS agent. Bootstrap and installer environments cannot be full members.
Reachability is `online`,
`stale`, or `offline`. A removed cluster record remains historical and is not an
active member. A new explicit join creates a new membership epoch for the same ID.
An `awaiting_approval` join request leaves membership `unjoined`; it is not a
pending member. Request records and approval bindings live in
[identity and trust](identity-and-trust.md#enrollment-and-trust-boundary).

Endpoint records use `service: api|nats_route|nats_leaf` and `transport: http|tls`.
All HTTP routes use `api`; NATS routes/leaves use TLS.
Advertise actual ports. HTTP reachability is not cryptographic peer authentication;
NATS verifies peer keys, caches verify output signatures, and removal verifies its payload. `hosts endpoints <host>` reads
`GET /api/v1/hosts/{id}`: its `connectivity` list contains
`{peer_id, service, selected_address, state, reason}`; address/reason may be null.

## Health status

V1 exposes host and cluster health independently of membership and reachability.

```json
{
  "status": "degraded",
  "observed_at": "2026-10-20T12:00:00Z",
  "reasons": [{"code": "SYSTEMD_UNIT_FAILED", "resource": "example.service", "message": "Service failed"}]
}
```

Host checks cover failed systemd units, essential Nixmox/NATS services, and writable
Nixmox state storage. `healthy` means checks passed; `degraded` means nonessential
service/capability failures; `unhealthy` means an essential check failed. Missing,
unsupported, or stale observations produce `unknown`, never assumed healthy.
Report check-specific reasons and preserve the timestamp of the last observation.

Cluster health includes quorum/control availability and full-member health:
unavailable quorum is `unhealthy`; a nonhealthy or unreachable full member
makes an otherwise available cluster `degraded`. A stale cluster snapshot is
`unknown`. Pending configuration alone is not a health failure.

Refresh on the normal inventory cadence, service changes, and deployment completion;
stream versioned updates over NATS and the common UI event API. Include health in
host reads and `/cluster/status`. CLI: `nixmox hosts show cedar --json` and
`nixmox cluster status`. Browser/TUI show badges on host lists and cluster overview,
with reasons and observation time in host details. Health is informational in v1;
it does not initiate deployment, restart, or automatic rollback.

## Live host telemetry

Browser and TUI Hosts → host details → Telemetry show CPU usage/load, memory/swap,
filesystem space, disk I/O, per-interface network rates, and uptime as current values
and recent sparklines, including on pending installers/bootstrap agents.
See [collection and delivery](technical-requirements.md#host-metrics).

`GET /api/v1/hosts/{id}/telemetry` returns the latest sample:

```json
{
  "schema_version": 1,
  "host_id": "n_cedar",
  "membership_epoch": 2,
  "stream_id": "telemetry_01", "sequence": 42,
  "observed_at": "2026-10-21T14:00:00Z", "freshness": "live",
  "interval_seconds": 0.5, "uptime_seconds": 86400,
  "cpu": {"utilization": 0.25, "logical_count": 8, "load_average": [0.8, 0.6, 0.4]},
  "memory": {"total_bytes": 17179869184, "available_bytes": 12884901888, "used_bytes": 4294967296},
  "swap": {"total_bytes": 4294967296, "used_bytes": 0},
  "filesystems": {"observed_at": "2026-10-21T14:00:00Z", "interval_seconds": 10,
    "freshness": "live", "items": [{"mount": "/", "total_bytes": 512000000000,
      "used_bytes": 128000000000, "available_bytes": 380000000000}]},
  "disks": [{"device": "nvme0n1", "read_bytes": 104857600, "write_bytes": 52428800,
    "read_bytes_per_second": 1048576, "write_bytes_per_second": 0}],
  "network": [{"interface": "enp1s0", "rx_bytes": 1073741824, "tx_bytes": 536870912,
    "rx_bytes_per_second": 2097152, "tx_bytes_per_second": 1048576}],
  "unavailable": []
}
```

CPU utilization is aggregate busy time across logical CPUs, normalized to 0–1;
idle and I/O wait are excluded. Load averages cover 1/5/15 minutes. Linux memory
used is total minus available. Filesystem available excludes reserved space, so
used + available need not equal total. Exclude pseudo-filesystems and duplicate
mounts; keep per-device/interface counters separate to avoid double-counting.
Rates use monotonic elapsed time and counter deltas; first samples and counter
resets have null rates. Unsupported/failed measurements are null with field/reason
entries in `unavailable`, never fabricated zeroes.

Follow updates through the [SSE topic](api-and-interfaces.md#reads-and-events)
`hosts.telemetry`, scoped by `host_id`. Telemetry never increments host
configuration/review versions.

Each metric group becomes stale after `max(3 seconds, 3 × its sampling interval)`
without a successful sample, even if heartbeats continue. Snapshot freshness covers
fast metrics; filesystem freshness and collection time remain independent. Reusing
cached filesystem values in fast snapshots must not advance their timestamp.
Freeze displayed rates/uptime with a stale label on stream loss; reconnect reads a
fresh snapshot. Known hosts without a sample return `freshness: unknown` and null
measurements. Clients retain at most five minutes of viewed history in memory;
gaps remain gaps. Durable history belongs to an optional external metrics backend.
CLI: `nixmox hosts telemetry cedar --json`; `--watch` follows the same samples.

## Discovery and enrollment

Nodes advertise their ID/name/endpoints; clusters advertise cluster ID/name and
reachable gateways. Discovery records expire and are hints, not identity proof.
Configured address plus cluster name submits a join request with retry backoff;
cluster approval is required. A name mismatch fails explicitly. After admission,
persist cluster ID and trust data. Retries reuse an unexpired request; rejection or
expiry requires an explicit new attempt, not an automatic approval-request loop.

```sh
nixmox discovery list
nixmox cluster create homelab --wait
# Submit from the unjoined node; --wait remains awaiting approval:
nixmox --endpoint http://cedar:63170 cluster join --address alder:63170 --name homelab --wait
# In another console, approve the exact request shown by the cluster:
nixmox --endpoint http://alder:63170 cluster requests list
nixmox --endpoint http://alder:63170 cluster requests approve join_01 --version 1 --wait
# Reject a different request:
nixmox cluster requests reject join_02 --version 1 --wait
# Alternative: initiate from an existing cluster console:
nixmox --endpoint http://alder:63170 cluster add --discovery discovery_cedar --wait
nixmox hosts assign cedar --repository lab --configuration cedar --wait
nixmox hosts show cedar
```

| Command type | Payload |
| --- | --- |
| cluster.create | name |
| cluster.join | address, expected_cluster_name |
| cluster.request.approve | request_id; expected `join_request:<id>` version; binds stored key/expiry |
| cluster.request.reject | request_id; expected `join_request:<id>` version |
| cluster.add | discovery_id, expected_node_id, expected_key_fingerprint |
| host.assign | host_id, repository_id, configuration, optional ref_id; expected host/configuration versions |
| host.adopt | host_id, candidate_id, confirm_host; expected host version |
| cluster.leave | host_id, membership_epoch; expected host version |
| host.remove | host_id, force (must be true), confirm_host, membership_epoch; expected host version |

Assignment defaults `ref_id` to the selected configuration's tracked ref, resolved
under its expected version. `hosts assign ... --ref <ref>` overrides it explicitly;
the selected ref must export that configuration. Reassignment changes desired
state, never running evidence.

`nixmox hosts adopt cedar --candidate cand_01 --confirm-host cedar --wait` verifies
that the running output equals the candidate's pinned output and that the permanent
Nixmox service is present. Collect evidence from the authenticated target; never
accept a caller's assertion of its running output. Mismatch blocks adoption without
activation. Browser/TUI Verify running configuration selects the same candidate
and confirms the host. Adoption preserves the desired binding and reports drift.

Browser: unjoined Clusters → homelab → Request join → Awaiting approval. Existing
cluster: Hosts → Join requests → inspect node/address/key → Approve or Reject.
Alternative: Discovery → cedar → Add to homelab admits immediately without a
node-side prompt. CLI Add resolves/reviews the discovered key before submission;
scripts supply `--key-fingerprint`. TUI uses the same screens/actions. The unjoined
[Clusters flow](host-onboarding.md#clusters-joining-and-headless-setup) is shared
by installer and installed nodes, including timeout, Search again, and live updates.
Discovery advertises the unjoined node's browser URL as well as enrollment endpoints.
Join addresses use the peer's advertised HTTP port, shared with its console.
After approval/Add, both show
Hosts → cedar → Pending → Assign configuration. A preconfigured installed host
uses Verify running configuration instead of silently assuming assignment is proof.

Installed hosts expose [Apply latest and Install](deployment-and-rollback.md#operation-boundaries)
on host lists/details. Disabled actions show capability reasons.

## State transitions

| Trigger | Before → after | Required evidence |
| --- | --- | --- |
| Request join | unjoined → unjoined | Expiring request bound to node key; no broker access |
| Approve request / cluster Add | unjoined → pending | Cluster decision bound to exact key; issued scoped credentials |
| Reject/expire request | unjoined → unjoined | No active membership or issued credentials |
| Create cluster | unjoined → pending | Creator authorizes provisional standalone control plane |
| Assign configuration | pending → pending | Valid repository/name binding |
| Verified install/deploy/adoption | pending → full | Exact source/output evidence and returning node identity |
| Disconnect/reconnect | membership unchanged | Reachability only changes |
| Change desired config on full host | full → full | Show drift; preserve previous deployed association |
| Planned entry into installer | full → pending | Drain durable services with quorum checks; verified handoff; retain identity/configuration intent |
| Graceful leave | joined → unjoined | Membership cleanup and credential revocation |
| Force remove | Cluster membership → removed; target → unjoined on authenticated notice | Manual quorum-committed removal bound to node ID and membership epoch |

Pending nodes use non-voting leaf participation and are not durable replicas.
They can stream assigned commands, observations, logs/progress, and results in both
directions under [leaf permissions](identity-and-trust.md#leaf-streaming).
The first creator can host a provisional standalone control plane while pending;
show “No full members” until its configuration is deployed or adopted. Do not
require an already-full member to complete the first deployment.

## Quorum and removal

Proposed topology: one durable broker initially; promote to three eligible full
members when available. Two full hosts do not imply two-replica JetStream fault
tolerance. After three-replica promotion, quorum loss
blocks new cluster-scoped durable mutations; local bootstrap operations retain
their node-local scope. Never silently scale down because nodes disappeared.
Graceful removal coordinates explicit replica changes. Explain durability in UI.
The recovered implementation contract below records the old lifecycle; do not
replace it with a self-routed singleton or confuse stream replication with
metadata Raft membership.

### Recovered promotion/demotion logic from the old implementation

Source: the parent of deletion commit `938218846025a87f702dd49307dd7d12d85a5e1c`,
namely `886fb663eed35ea96a55e324e7ade2398b11e4cd` (`93821884^`). Paths and symbols
in this section refer to that historical tree, not the current implementation.
It used `nats-server/v2 v2.14.4` and `nats.go v1.52.0`. These are source-derived
behaviors and test assertions, not a claim that the old tests were rerun.

The old code distinguished permanent nodes from temporary installers; it did
not have the rewrite's deployment-verified pending/full promotion gate.
Permanent join initiated routing directly. Preserve the following transport
lifecycle when applying the rewrite's separate membership eligibility rules.

| Transition/state | Embedded broker / metadata Raft | Application streams |
| --- | --- | --- |
| Create cluster, creator alone | Standalone; no NATS `Cluster` block or metadata Raft group | R1 |
| First permanent peer joins | Both sides establish real routes; two-member metadata Raft | R1, never R2 |
| At least three online permanent peers | Routed metadata Raft group | Explicit R1 → R3 updates |
| Members become unreachable | Keep configured routes/membership and Raft size | Keep R3; no timeout-based downscale |
| Planned three → two members | Commit replica reduction before Raft peer removal; retain two-member metadata Raft | R3 → R1 |
| Planned two → one members | Last survivor keeps cluster name but returns to standalone; no metadata Raft | R1 |

**Startup and promotion ordering**

1. `cluster.Supervisor.CreateCluster` persisted the cluster name in `node.json`
   without restarting the standalone broker. `Start` restored routed mode only
   when a non-installer had a saved cluster name **and nonempty routes**.
   `natsx.NewOptions` required a real peer route for clustered mode. Its comment
   explicitly warned that self-routes stall election.
2. `Supervisor.AcceptJoin` validated the cluster name, node-name collision, and
   joiner's route, then persisted the route. On the first inbound join it
   restarted the embedded broker in clustered mode using the joiner's address.
   It returned bootstrap/known-peer information without waiting for meta-leader
   readiness. `JoinCluster` then persisted the joiner's identity/routes and
   started its routed broker. Blocking the seed's join response on election
   would prevent the other side from starting the route needed for that election.
3. `Supervisor.startLocked` waited for readiness **asynchronously** using
   `natsx.WaitReadyContext`: poll `JetStream.AccountInfo` on leaders or followers,
   with two-second attempts, 500 ms delays, and retryable 90-second windows.
   It did not require every server to become the metadata leader. Generation
   cancellation and an embedded-server/generation check prevented stale readiness
   callbacks from rearming a superseded transport.
4. `OnReconfigure` rearmed `cluster.Runtime` and `messaging.Bus`. Only the broker
   and its dependent subscriptions/watchers restarted; HTTP, job runner, cron,
   and running jobs survived. Runtime retried bucket creation and host watches
   because account readiness can precede stream/consumer readiness.
5. `Runtime.arm` ran `ReplicaScaler.Check` every minute. The scaler counted
   `Online && !Temporary` inventory entries and, at three or more, requested R3
   for `KV_hosts`, `KV_applylog`, and `nixmox_commands`. `natsx.ScaleStream` read
   the current stream config, changed only `Replicas`, and called `UpdateStream`;
   matching counts were a no-op and R2 was rejected. The periodic scaler never
   reduced replicas. Its liveness count triggered an attempt, not proof of quorum
   or completed replica catch-up.

**Storage boundary — the old solution was separation, not in-place migration**

`internal/natsx/embed.go:NewOptions` selected `D/nats-standalone` versus
`D/nats-clustered`. Its comment explicitly said reusing a standalone store wedges
clustered Raft metadata and that standalone state was disposable. First routing
therefore opened a separate clustered store; it did **not** import the old store
or implement a lossless JetStream backup/restore migration. Missing buckets and
the command stream were created at R1 after readiness.

Local jobs/logs and command receipts survived broker replacement independently.
`cluster.SwitchingRecordStore` always wrote apply history locally and mirrored
new records to cluster KV when available. This was not a general replay/copy of
all old KV entries or queued commands. The last survivor's standalone transition
selected its standalone directory again; it did not copy clustered streams back.

The rewrite now puts authoritative membership/approval/idempotency records in
JetStream, so those records cannot simply be declared disposable. Carry forward
separate stores and nonblocking two-sided route establishment, but explicitly
preserve/reconcile authoritative records before a store switch. The old code
supplies the topology lifecycle, not a lossless transfer implementation for the
rewrite's different persistence model. Do not claim either that in-place reuse
is safe or that the old code solved this by running singleton metadata Raft.

**Planned demotion/removal ordering**

1. `server.hostAction` collected non-temporary peers and called
   `Supervisor.PrepareLeave(remaining)` before `Leave`. For fewer than three
   remaining members, `PrepareLeave` reduced the existing hosts/applylog KV
   streams and command stream to R1. Errors stopped the leave; no R2 intermediate
   was requested. This was an explicit planned change, not a liveness reaction.
2. `Supervisor.Leave` detached cluster runtime/transport hooks, then, while the
   routed broker was still connected, called `EmbeddedServer.RemovePeer` with
   a 20-second deadline. A failure rearmed the previous runtime and aborted.
3. `natsx.RemovePeer` used a loopback system-account connection and NATS's own
   administration APIs. If the departing local server was metadata leader, it
   first requested `JSApiLeaderStepDown`; otherwise it could remove itself and
   lose the ability to acknowledge its RPC. It then requested `JSApiRemoveServer`
   with the server name. Already-not-a-member meant an earlier attempt committed;
   in-flight member changes, transient server errors, missing responders, and
   timeouts were retried (two-second removal attempts, 250 ms retry delay).
4. Only after committed peer removal did `Leave` cancel the generation, stop the
   broker, archive `nats-clustered` as `nats-clustered.left-<timestamp>`, persist
   an unclustered identity with `Left: true`, and start standalone. Local history
   remained; `Left` suppressed automatic rejoin across restart.
5. The handler notified survivors through `/api/v1/cluster/forget`, reporting
   failures as cleanup warnings. `ForgetHost` purged the departed inventory key;
   `ForgetRoute` merged saved/known routes, removed the departed and self routes,
   and persisted the result. With peers remaining, `ReloadOptions` changed routes
   without restarting surviving Raft. With none remaining, it restarted standalone
   while preserving the survivor's cluster name. Explicit rejoin cleared `Left`.

Temporary installers had JetStream disabled and no store. Their leaf link did
not share `$SYS` or extend the Raft group (`ConnectInstaller` and
`TestInstallerLeafUsesCoordinatorJetStream`). Merely connecting a temporary node
was not a promotion.

**Existing verification to preserve**

- `internal/cluster/leave_test.go:TestLeaveAndRejoin` deliberately made the
  departing node metadata leader and exercised two leave/rejoin cycles, checking
  two-member Raft after join and no metadata Raft on the last survivor.
- `e2e/run.py:topology` checked metadata group size/leader separately from stream
  replica counts, and checked follower catch-up from each group's leader.
- `e2e/run.py:quorum_probe` required a commit ACK on the **existing replicated
  hosts stream**, not a separate R1 probe stream. NATS PING/PONG before and after
  the probe distinguished write-quorum failure from a broken transport.
- `quorum_loss` first required healthy three-member Raft and all three streams
  at R3, killed two VMs, and repeatedly checked unchanged topology and rejected
  writes for 75 seconds (longer than the scaler interval). Restoring one peer
  had to restore acknowledged writes; the third then had to catch up.
- `teardown` checked planned 3 → 2 → 1, R3 → R1 before shrinking, standalone
  survivors, restart suppression on removed nodes, rejoin, and fresh-cluster reuse.

```sh
nixmox cluster status
nixmox hosts leave cedar --wait
```

Browser: Hosts → cedar → Membership → Leave → review affected replicas → Confirm.
TUI: same Membership action and review. Remove routes/voting membership, revoke
credentials, archive cluster storage, preserve node ID and task history. Return
cleanup warnings as structured results. Explicit leave suppresses configured
join requests until an explicit join clears the suppression. Rejoin needs fresh
approval; retain node ID but never reactivate old credentials. Follow the
[state inventory](identity-and-trust.md#generated-and-preserved-state) for cleanup
and issuer preservation; block issuer removal while it is the only issuer.

Force removal is a manual v1 operation requiring surviving quorum, never an
automatic response to timeout or a way to bypass quorum loss.

```sh
nixmox hosts remove cedar --force --confirm-host cedar --wait
```

Browser/TUI: Hosts → cedar → Membership → Force remove → review → confirm host.
Submit `host.remove` with `host_id`, `force: true`, confirmed host identity, and
expected membership epoch/version. Persist a removal record, revoke membership,
and reconcile routes/replicas without contacting the target. Preserve historical
configuration/task records; do not claim the absent machine's disks were cleaned.
Force removal retains the sole-issuer restriction above.

A returning node becomes unjoined only after verifying a cluster-signed removal
record for its cluster, node ID, and current membership epoch. Suppress configured
autojoin until explicit Join/Add; re-enrollment gets fresh credentials and begins
pending. Preserve local node identity, configuration intent, and installed system;
quarantine old cluster storage so it cannot rejoin broker replication. A timeout,
TLS failure, or unauthenticated rejection only means disconnected, not removed.
Use the [removal-response contract](identity-and-trust.md#authenticated-removal-response).
Cluster reuse must not resurrect old broker membership or routes.
