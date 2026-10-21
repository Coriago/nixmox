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
  "endpoints": [{"address": "192.168.1.32:63173", "interface": "enp1s0", "verified": true}],
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
# Alternative: initiate from an existing cluster console:
nixmox --endpoint http://alder:63170 cluster add --discovery discovery_cedar --wait
nixmox hosts assign cedar --repository lab --configuration cedar --wait
nixmox hosts show cedar
```

| Command type | Payload |
| --- | --- |
| cluster.create | name |
| cluster.join | address, expected_cluster_name |
| cluster.request.approve | request_id, expected_version; binds stored key/expiry |
| cluster.request.reject | request_id, expected_version |
| cluster.add | discovery_id, expected_node_id, expected_key_fingerprint |
| host.assign | host_id, repository_id, configuration, ref_id |
| host.adopt | host_id, pinned source, observed output; explicit verification |
| cluster.leave | host_id, expected membership epoch |

Browser: unjoined Clusters → homelab → Request join → Awaiting approval. Existing
cluster: Hosts → Join requests → inspect node/address/key → Approve or Reject.
Alternative: Discovery → cedar → Add to homelab admits immediately without a
node-side prompt. CLI Add resolves/reviews the discovered key before submission;
scripts supply `--key-fingerprint`. TUI uses the same screens/actions. The unjoined
[Clusters flow](host-onboarding.md#clusters-joining-and-headless-setup) is shared
by installer and installed nodes, including timeout, Search again, and live updates.
Discovery advertises the unjoined node's browser URL as well as enrollment endpoints.
After approval/Add, both show
Hosts → cedar → Pending → Assign configuration. A preconfigured installed host
uses Verify running configuration instead of silently assuming assignment is proof.

Installed hosts with a configuration binding expose
[Apply latest](deployment-and-rollback.md#apply-latest) on list rows and detail
pages in browser/TUI, including the per-operation Auto-approve toggle. This starts
the complete build/diff/deploy task; pending installation uses the onboarding flow.
The Install action submits `deployment.install` for first or fresh installation.
Show it disabled with capability reasons when the running OS has no supported
installer-entry method; agent connectivity alone does not imply installability.

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
tolerance. Each node fetches Git bases from the remote independently; JetStream
stores local changes. After three-replica promotion, quorum loss
blocks new durable mutations; never silently scale down because nodes disappeared.
Graceful removal coordinates explicit replica changes. Explain durability in UI.

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

A returning node becomes unjoined only after verifying an authenticated removal
response for its cluster, node ID, and current membership epoch. Suppress configured
autojoin until explicit Join/Add; re-enrollment gets fresh credentials and begins
pending. Preserve local node identity, configuration intent, and installed system;
quarantine old cluster storage so it cannot rejoin broker replication. A timeout,
TLS failure, or unauthenticated rejection only means disconnected, not removed.
Use the [removal-response contract](identity-and-trust.md#authenticated-removal-response).
Cluster reuse must not resurrect old broker membership or routes.

Acceptance: create with no full members; request/approve/reject/expire; cluster Add
without node confirmation; conflicting key/cluster rejection; preconfigured pending
host; promotion; LAN endpoint change; stale inventory after restart; quorum loss;
graceful removal and fresh rejoin with unchanged node identity.
