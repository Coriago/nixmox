# Identity enrollment and credentials

One administrator role operates all interfaces. Network access to the management
console grants that authority; there are no human accounts or role hierarchy.
Machine authentication is separate from that interface access model.

## Identity record and lifecycle

```json
{
  "schema_version": 1,
  "node_id": "n_cedar",
  "key_id": "key_01",
  "public_key": "BASE64_PUBLIC_KEY_FIXTURE",
  "cluster_id": "c_lab",
  "membership_epoch": 2,
  "cluster_fingerprint": "SHA256_CLUSTER_FINGERPRINT_FIXTURE"
}
```

Generate a random node ID and private key at first boot; store the private key in
owner-only local state, not this public record. Do not derive identity from MAC,
IP, hostname, firmware UUID, disk serial, SSH host key, or Tailscale identity.
Hardware values are recognition/recovery hints, never takeover authorization.

Preserve Nixmox identity locally through installation. A freshly booted installer
without preserved identity gets a new node ID; replacement of an old record is an
explicit operation. Cloned installed disks require identity reset. Conflicting
simultaneous identity claims block affected operations and surface a diagnostic.
Leaving clears membership credentials and increments membership epoch on rejoin,
while keeping node identity and local history.

An SSH-bootstrap agent, kexec/media installer, and installed agent use the same
identity through an authorized handoff. Stage a scoped one-use handoff outside
erase targets; verify node ID and boot session on return. Temporary agents remain
pending and cannot expose cluster credentials. SSH host-key verification authorizes
initial agent delivery; it does not replace Nixmox identity or become the node ID.

```sh
nixmox identity show --json
nixmox hosts endpoints cedar
nixmox identity reset --confirm-host cedar
```

Reset is local-only and allowed only while unjoined, with explicit review of lost
continuity. Browser/TUI: unjoined Settings → Identity → Reset → confirm. Joined
hosts must leave first. No endpoint may reset a different node by name alone.

## Address handoff example

```json
{
  "node_id": "n_cedar",
  "version": 9,
  "endpoints": [
    {"address": "192.168.1.32:63172", "interface": "enp1s0", "priority": 20, "verified": true},
    {"address": "100.90.0.32:63172", "interface": "tailscale0", "priority": 10, "verified": true}
  ]
}
```

1. Cedar installs with its original ID and returns over LAN.
2. Its virtual interface appears; the agent advertises a new candidate over an
   authenticated session. Each peer independently tests reachability and identity.
3. Peers prefer tailscale0 according to configured interface order; no task/config
   reference changes. Close obsolete routes only after a usable replacement exists.
4. If all routes fail, show disconnected and retain receipts. Reconnect with the
   same task/command IDs; do not duplicate an uncertain operation.

Different peers can select different paths. `allowFallback=false` blocks fallback
to a less-preferred interface after selection. Define the initial required interface
through the first preferred entry; wait visibly if unavailable. Browser origin
never silently changes with agent routing.

Browser: Hosts → cedar → Connectivity lists verified endpoints and active peer
paths. TUI: Hosts → cedar → Connectivity. The UI explains why a route is unavailable
or excluded by policy. Discovery is not assumed to cross virtual networks.

## Enrollment and trust boundary

Configured cluster address/name triggers autojoin; unconfigured nodes remain
unjoined and discoverable. A node can join a discovered cluster or a cluster can
add a discovered node. Pin cluster ID and public identity after first enrollment.
A name is a label, not a secret or proof of identity.

Pending nodes are associated but non-voting until configuration is assigned and
deployment verified. They receive their tasks, needed source, and inventory access;
not cluster credential storage or broker administration. Full nodes can provide
cluster services subject to capabilities. Membership rules live in
[host lifecycle](host-inventory-and-lifecycle.md).

**Machine-auth proposal:** use managed TLS on agent links and NATS-authenticated
identities with subject/account permissions. Apply authentication to server routes,
leaf connections, and clients, not only the HTTP gateway. Automatically pin trust
on first contact within the trusted management network. Optional join tokens or
pinned cluster fingerprints can harden bootstrap without introducing human users.

This does not protect an unrestricted administrator API from someone with network
access. Restrict its listeners to configured management interfaces. Never describe
autojoin by address/name as protection against a hostile first-contact peer.
[NATS authentication](https://docs.nats.io/learn/security/authentication-basics)

## Credential record and operations

```json
{
  "id": "cred_github",
  "name": "github-writer",
  "kind": "git-http-token",
  "scope": {"repository_ids": ["r_lab"]},
  "replicas": [{"node_id": "n_alder", "state": "available", "source": "runtime_file"}],
  "value": null
}
```

Public reads never return the value. Secret storage is separate from ordinary
JetStream state/artifacts; only capability metadata is replicated. Do not use a
node's interactive SSH agent or copy its credentials onto an installer.

```sh
nixmox credentials import github-writer --kind git-http-token --node alder --file /run/keys/token
nixmox credentials list
nixmox repositories credential set lab github-writer
nixmox credentials check github-writer --repository lab --wait
```

Browser: Settings → Credentials → Add → select storage node → hidden value field
→ Save → Test access. TUI mirrors these controls with hidden input. Module-managed
credentials show their source and cannot be overwritten by UI. Import requires
local or encrypted gateway transport; do not send tokens over a plaintext LAN URL.

Types: `credential.import`, `credential.check`, `credential.remove`; values are
sensitive request data and must use a dedicated secure ingress that stores the
secret locally before dispatching a value-free durable command. No raw secret may
enter command envelopes, receipts, traces, or logs. Removal checks repository and
updater references; it does not silently break configured dependents.

Any console can route a credential-dependent operation to an available full member
that holds it. If none is available, return `CREDENTIAL_UNAVAILABLE`; local Git and
builds remain usable if source/dependencies are present. For the first implementation,
provision a second replica independently through a runtime-secret reference or UI
import. This avoids making encrypted cross-node secret distribution a prerequisite.
A replica is not a permanently designated leader.

On leave, clear managed credential copies and revoke membership. Provider-side
rotation is needed for copies on lost/unreachable machines. Nixmox cannot guarantee
erasure on a disconnected host.

## Implementation gates

**Decision required before production machine trust:** key format and rotation,
TLS issuance/renewal, cluster signing-key custody/failover, bootstrap pin recovery,
and pending-node subject permissions. Do not implement an ad-hoc cryptographic
protocol to fill these gaps. Manual independent credential replication is the
proposed v1 baseline; automatic encrypted replication is deferred.

Acceptance: identity survives installation and address changes; wrong key at a
remembered endpoint is rejected; pending node cannot access credentials; removal
revokes membership; credential loss affects only dependent operations; no secrets
appear in Git, JetStream command history, diagnostic logs, or error responses.
