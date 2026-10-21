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

## Discovery and enrollment

Nodes advertise their ID/name/endpoints; clusters advertise cluster ID/name and
reachable gateways. Discovery records expire and are hints, not identity proof.
Configured address plus cluster name triggers autojoin with retry backoff. A name
mismatch fails explicitly. After first contact, persist cluster ID and trust data.

```sh
nixmox discovery list
nixmox cluster create homelab --wait
# Run against the unjoined node's console:
nixmox --endpoint http://cedar:63170 cluster join --address alder:63170 --name homelab --wait
# Alternative: initiate from an existing cluster console:
nixmox --endpoint http://alder:63170 cluster add --discovery discovery_cedar --wait
nixmox hosts assign cedar --repository lab --configuration cedar --wait
nixmox hosts show cedar
```

| Command type | Payload |
| --- | --- |
| cluster.create | name |
| cluster.join | address, expected_cluster_name |
| cluster.add | discovery_id, expected_node_id |
| host.assign | host_id, repository_id, configuration, ref_id |
| host.adopt | host_id, pinned source, observed output; explicit verification |
| cluster.leave | host_id, expected membership epoch |

Browser: Discovery → cedar → Add to homelab, or unjoined Clusters → homelab
→ Confirm joining. TUI uses the same screens and action labels. The unjoined
[Clusters flow](host-onboarding.md#clusters-joining-and-headless-setup) is shared
by installer and installed nodes, including timeout, Search again, and live updates.
Discovery advertises the unjoined node's browser URL as well as enrollment endpoints.
Both then show
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
| Create/join | unjoined → pending | Recorded cluster association |
| Assign configuration | pending → pending | Valid repository/name binding |
| Verified install/deploy/adoption | pending → full | Exact source/output evidence and returning node identity |
| Disconnect/reconnect | membership unchanged | Reachability only changes |
| Change desired config on full host | full → full | Show drift; preserve previous deployed association |
| Planned entry into installer | full → pending | Drain durable services with quorum checks; verified handoff; retain identity/configuration intent |
| Graceful leave | joined → unjoined | Membership cleanup and credential revocation |

Pending nodes use non-voting leaf participation and are not durable replicas.
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
autojoin until an explicit join clears the suppression.

Force-removing an unreachable node is **Decision required**; first-release behavior
must refuse it with an actionable explanation rather than claim safe eviction.
Cluster reuse must not resurrect old broker membership or routes.

Acceptance: create with no full members; both join directions; preconfigured pending
host; promotion; LAN endpoint change; stale inventory after restart; quorum loss;
graceful removal and fresh rejoin with unchanged node identity.
