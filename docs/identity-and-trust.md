# Identity enrollment and credentials

One administrator role operates all interfaces. Network access to the management
console grants that authority; there are no human accounts or role hierarchy.
Machine authentication is separate from that interface access model.
CLI, TUI, and browser use HTTP, including secret submission; no human client needs
certificates. Console traffic is unencrypted. Automated TLS protects backend
daemon-to-daemon and NATS connections only.
V1 requires no preseeded enrollment secret or manually managed NATS credentials.
The [state inventory](#generated-and-preserved-state) owns security paths and
preservation rules; other documents reference it.

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

Generate a random node ID and daemon TLS key at first boot. `key_id` and
`public_key` identify that replaceable key; no extra identity-signing key is needed.
Store the private key in owner-only local state, not this public record. Do not
derive identity from MAC, IP, hostname, firmware UUID, disk serial, SSH host key,
or Tailscale identity.
Hardware values are recognition/recovery hints, never takeover authorization.

Preserve Nixmox identity locally through installation. A freshly booted installer
without preserved identity gets a new node ID; replacement of an old record is an
explicit operation. Cloned installed disks require identity reset. Conflicting
simultaneous identity claims block affected operations and surface a diagnostic.
Leaving clears membership credentials and increments membership epoch on rejoin,
while keeping node identity and local history.
Key rotation preserves `node_id`; changing a public key or claiming an existing
ID without an authorized rotation/recovery never transfers that identity.

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

Reset applies only to the addressed daemon's own identity while unjoined, with
explicit review of lost continuity. Browser/TUI: unjoined Settings → Identity →
Reset → confirm. Remote browser/CLI access to that daemon is allowed; this is not
a loopback-only action. Joined hosts must leave first. A gateway cannot reset a
different node by naming it.
`identity.reset` takes `{confirm_host}` and the expected local identity version.
It uses node-local admission; confirmation must
match that node's displayed name/ID. Persist the replacement ID/key and reset receipt
atomically; retrying the same command must not generate another identity. Retain
the receipt outside identity key storage so it remains queryable after reset.

### Reusable installer identity

Choose identity before advertising discovery or starting enrollment:

| Installer entry | `node_id` behavior |
| --- | --- |
| Fresh USB/SD boot, no authorized handoff | Generate a new ID and machine keys in ephemeral installer state |
| Same media moved to another host | Generate a new ID; never reuse the previous session's identity |
| Existing Nixmox host enters via authorized kexec/reboot handoff | Restore its ID, scoped credentials, and trust before starting the agent |
| Installation completes | Preserve the active installer's ID and required keys into the installed system |
| Same host boots media again without a handoff | New ID; v1 does not recover a lost identity from hardware |

Installer state survives daemon restarts within a boot, but is not written back
as the reusable medium's default identity. Retained recovery images follow the
same rule. Do not automatically import leftover handoff files or identities found
on attached disks. A plain `node_id` override cannot claim an existing host.

An expected handoff must match the authorized transition, node identity/key, and
one-use handoff record; the installer reports a fresh boot-session ID. Missing,
invalid, or consumed handoffs block that transition rather than silently enrolling
a replacement node. Keep recovery checkpoints outside erase targets and consume
handoffs once. The [preservation inventory](#generated-and-preserved-state) defines
the transferred values; hardware identifiers remain hints, never authorization.

## Address handoff example

```json
{
  "node_id": "n_cedar",
  "version": 9,
  "endpoints": [
    {"service": "nats_route", "transport": "tls", "address": "192.168.1.32:63172", "interface": "enp1s0", "priority": 20, "verified": true},
    {"service": "nats_route", "transport": "tls", "address": "100.90.0.32:63172", "interface": "tailscale0", "priority": 10, "verified": true}
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

```text
Node → request join → awaiting cluster approval → approve → pending leaf
Cluster → discover unjoined node → Add ────────────────────┘
pending leaf → verified install/deploy/adoption → full member
```

Configured address/name submits a join request; it never approves one. Awaiting
approval is a request state: the node remains unjoined, with no cluster broker or
data access. Cluster-side Add supplies approval immediately; no node-side permission
is required. Joined nodes cannot be claimed by another cluster. Cluster creation
authorizes its creator's provisional standalone control plane.

```json
{
  "id": "join_01", "version": 1, "node_id": "n_cedar",
  "name": "cedar", "observed_address": "192.168.1.32",
  "key_fingerprint": "SHA256_DAEMON_KEY_FIXTURE",
  "state": "awaiting_approval", "expires_at": "2026-10-20T12:10:00Z"
}
```

Approve the exact request/version/key after proof of possession; name/address are
display hints. Bind issuance to its node ID, cluster ID, membership epoch, and
approved key. Nixmox chooses certificate roles/names; never sign caller-selected
privileges. Requests expire after ten minutes; bound request sizes/rates and dedupe
retries. A changed key or expired/rejected request requires a new explicit attempt.

Bootstrap uses a limited mutual-TLS exchange with locally generated keys and first-contact
trust; it supports request submission/status and certificate provisioning only.
No preinstalled token, CA secret, Git credential, or SOPS setup is needed. Pin the
accepted cluster identity after admission. Approval controls admission but does
not defeat an active attacker intercepting first contact on the management network.

Pending nodes receive scoped credentials for onboarding, not full membership.
Full membership requires verified deployment/adoption; see
[host lifecycle](host-inventory-and-lifecycle.md).
The unrestricted management API remains administrative authority, including join
approval. Broker restrictions do not stop someone using that API; restrict its
listeners to management interfaces. Human/interface authentication is outside v1.

### Enrollment HTTP exchange

These machine-only bootstrap routes use `machine.port` (default 63175) with
automated TLS. Administrative `/api/v1/commands` and `/cluster/requests` reads stay
on the HTTP console; the CLI/TUI/browser asks its daemon to perform enrollment.
Before admission the node presents a locally generated self-signed daemon
certificate. The server presents its generated daemon certificate. TLS verifies
key possession on both sides; accepting a bootstrap certificate grants enrollment
access only, not cluster membership. Pin the server key for the enrollment attempt;
an unexpected key change fails that attempt. First-contact trust has the limitation
above. After admission pin the accepted cluster CA and peer identity.

| Endpoint | Request / response |
| --- | --- |
| `POST /api/v1/enrollment/requests` | Node identity, expected cluster name, and daemon/broker CSRs → own request record; stable request ID deduplicates retries |
| `GET /api/v1/enrollment/requests/{id}` | Request-owner read of state, expiry, cluster ID/fingerprint, and decision; no cluster inventory |
| `POST /api/v1/enrollment/requests/{id}/certificates` | Retrieve approved certificates, CA chain, membership epoch, and scoped grants |

Use standard TLS/X.509 and signed PKCS#10 CSRs; no custom HTTP signing or
certificate-encryption format is required.

1. Authenticate the request owner by the TLS client public key, never by a
   caller-supplied ID or certificate name. Verify both CSR signatures and require
   the daemon CSR key to match that TLS key.
2. Persist the request ID, owner key, node ID, expected cluster name, and exact
   daemon/broker CSRs as an immutable binding. Status and certificate retrieval
   require the same authenticated owner key; knowing a request ID grants no access.
3. Approve the exact request/version/binding. The cluster chooses certificate
   identities and pending-node permissions; caller-selected CSR roles/extensions
   cannot grant privileges. Changed keys require a new request and approval.
4. Issue node-specific certificates containing the approved public keys, signed by
   the cluster CA. Persist issuance before responding. Return those certificates,
   the public CA chain, membership epoch, and scoped grants over TLS. Private keys
   stay on the node; certificates need no additional encryption.
5. The node verifies the returned keys/identity against its approved request,
   persists the accepted cluster trust, and connects using its existing private
   keys and issued certificates. Mutual TLS proves possession on each connection;
   current membership and grants determine access.

The same request ID and binding returns the existing request or persisted issuance;
a different binding returns `IDEMPOTENCY_CONFLICT`. Restart or a lost response must
not create another membership or issuance. Expired/rejected requests cannot be
revived by retries; revoked memberships cannot retrieve credentials. Cluster-side
persistence/approval/issuance requires quorum. The joining node retains its task
locally while waiting. Add verifies the discovered key and uses this same flow
without a node-side prompt.

Bootstrap certificate acceptance is restricted to these enrollment routes on the
[machine listener](runtime-configuration.md#service-ports). Other machine routes
and NATS require their normal membership authentication; the HTTP console needs
no browser certificates. Integration tests verify key possession, request-owner
isolation, immutable approval bindings, and retry/restart behavior.

## Managed TLS and NATS permissions

Nixmox generates a cluster CA and separate daemon/client and broker TLS keys.
Use standard X.509/TLS libraries and certificate-to-user mapping; v1 requires no
NKeys, JWT hierarchy, shared broker password, or manual certificate configuration.
Daemon keys also identify agent machine links. All private keys are generated
locally; enrollment exchanges public keys/CSRs and certificates only.
Before enrollment, the local broker trusts only locally provisioned keys; this
permits standalone operation without granting cluster access. The machine listener
additionally permits the restricted bootstrap exchange defined above.

```text
Full daemon ── mTLS, loopback :63171 ── Full broker
                                          ⇅ mTLS :63172, admitted broker keys
                                      Full broker
                                          ⇅ mTLS :63173, scoped leaf permissions
Pending daemon ─ mTLS, loopback :63171 ─ Pending broker

Daemon ── managed mTLS :63175 ── Peer daemon (cache/credential transfers)
```

| Connection | Authentication and authorization |
| --- | --- |
| Daemon → local broker | Require daemon certificate; no anonymous publish, subscribe, or JetStream access, even on loopback |
| Full broker ↔ full broker | Verify TLS peer and explicit full-broker public-key allowlist in both directions; CA signature alone is insufficient |
| Pending broker → cluster | Map leaf certificate to that node's scoped account/permissions; enforce limits at the accepting cluster broker |
| Agent machine links | Verify pinned cluster trust, peer identity, current membership, and operation scope |

Configure TLS independently for clients, routes, and leaves; reject plaintext and
verification bypasses. Disable unused monitoring, profiling, WebSocket, MQTT, and
gateway listeners. Restrict network listeners to management interfaces.
[NATS TLS](https://docs.nats.io/learn/security/encryption),
[certificate pinning](https://github.com/nats-io/nats.docs/blob/master/running-a-nats-service/configuration/securing_nats/tls.md)

### Leaf streaming

Approved leaves stream continuously in both directions; non-voting does not mean
read-only or disconnected. They are not durable cluster replicas.

| Cedar may receive | Cedar may publish |
| --- | --- |
| Its assigned commands and task-scoped query replies | Its observations, heartbeat, activity, and execution results |
| Required source/artifact access and permitted inventory views | Logs/progress for tasks assigned to cedar; exact delivery acknowledgments |

For example, allow delivery on `nx.c_lab.command.n_cedar`, publishing to
`nx.c_lab.activity.n_cedar` and `nx.c_lab.result.n_cedar`, and explicit
`nx.c_lab.log.<assigned-task>` / `nx.c_lab.progress.<assigned-task>` subjects.
Full services own durable consumers and authorize deliveries/acks; do not grant
leaves arbitrary consumer creation, KV/Object Store access, `$JS.API.>`, `$SYS.>`,
other nodes' subjects, or unrestricted reply inboxes. Reconcile task-scoped grants
on assignment/completion. Validate task ownership in the service as well.

Logs/progress use Core NATS with snapshot/offset recovery; commands use durable
delivery. Required Git increments are exposed through scoped application requests;
Nix outputs stream directly through the authorized cache gateway. Backpressure and
reconnect behavior follow [NATS durability](technical-requirements.md#nats-namespace-and-durability).
[NATS permissions](https://docs.nats.io/learn/security/authorization)

Only authorized Nixmox service clients originate application commands. Full brokers
are trusted routing/replication infrastructure: v1 does not cryptographically prove
message origin against a compromised full broker. Likewise, credentials authenticate
their holder, not an executable; local root/key theft is outside this boundary.

### Issuance, rotation, and removal

V1 has one automatic issuer on the cluster creator; no automatic CA replication or
issuer failover. The creator is the sole provisional-control-plane exception to
pending-node restrictions. Protect/preserve its issuer state through installation.
Issuer outage blocks admission/renewal; existing valid connections can continue.
Block issuer removal or erasure unless its state is preserved through the operation.

Proposed defaults: Ed25519 keys, 30-day machine certificates, renewal after 20 days.
Renew only current authorized membership; distribute new public-key grants before
retiring old ones. Leave/removal updates allowlists and permissions, closes affected
connections, and denies renewal/reconnect. Do not claim revocation complete until
active accepting brokers apply it; returning brokers reconcile before serving.
Expiry alone is not revocation. Lost identity keys require a new identity and join;
v1 has no lost-key/pin recovery or issuer backup/restore workflow. Never silently
replace trust pins. Planned installation handoff still preserves existing keys/state.

### Authenticated removal response

After broker rejection, a node may query a known cluster peer's machine listener over HTTPS:
`GET /api/v1/membership-status?node_id=n_cedar&membership_epoch=2` endpoint.
Authenticate the server against the node's already pinned cluster trust and
authorized peer identity. Never learn replacement trust from a removal message,
discovery broadcast, redirect, or changed cluster name/address.

Return only `{cluster_id, node_id, membership_epoch, status, removal_version}`;
`removed` requires a durable quorum-committed removal record. Unknown/unavailable
state is not removal. This narrowly scoped read remains available without valid
client membership credentials, so a revoked node can learn its status; it grants
no NATS access or other cluster reads/mutations. Use a fresh TLS request, no cached
HTTP response. The node checks all identity/epoch fields before clearing membership;
an old response cannot remove a later enrollment. Unsolicited notices are hints
to perform this check, never authority to leave.

Persist confirmed removal and autojoin suppression before stopping old broker
participation and returning to unjoined state. Keep identity and the installed OS;
rejoining requires explicit enrollment and new authorization.

## Generated and preserved state

This is the authoritative storage layout. `D` means `services.nixmox.dataDir`
(default `/var/lib/nixmox`); `R` means `/run/nixmox`. Other specs reference these
paths. Values may originate in the installer before preservation into the installed OS.

### Persistent node values

| Value | Created or supplied | Stored / preserved |
| --- | --- | --- |
| `node_id` | Generated at first boot without existing state, or restored by authorized handoff | `D/identity.json`; preserve into installed OS and through managed reinstall, rotation, leave, and rejoin; see [installer identity](#reusable-installer-identity) |
| SSH host key | Generated per node; also provides its SOPS decryption identity | `/etc/ssh/ssh_host_ed25519_key` and `.pub`; restore before SSH/SOPS startup |
| Wi-Fi credentials, when needed | Supplied during network setup | Network backend's connection profiles; preserve the profiles needed after installation |
| NATS/TLS credentials | Keys generated locally; certificates issued automatically on admission and renewed | `D/pki/daemon/` and `D/pki/broker/`, each containing `key.pem` and `cert.pem`; preserve current credentials through installation |

`D/identity.json` contains the public identity record; private machine keys live
only under `D/pki/`. The SOPS age recipient is derived from the SSH public key;
no additional persistent age private key is required.

### Persistent cluster values

Cluster-wide describes ownership, not replication of every value to every node.

| Value | Created or supplied | Stored / preserved |
| --- | --- | --- |
| `cluster_id` | Generated once when creating the cluster | Cluster records and each joined node's `D/trust/`; unchanged across node reinstall/rejoin |
| Cluster CA | Generated at cluster creation; signs machine certificates | CA private key/certificate and issuance ledger in issuer-only `D/pki/issuer/`; public CA and trust pin in members' `D/trust/` |
| Membership and authorization | Recorded on approval/Add, promotion, rotation, and removal | Cluster records hold node IDs, membership epochs, approved public keys, and grants; local trust projection in `D/trust/` and enrollment receipt in `D/enrollment/` |

Preserve each node's cluster association/trust alongside its node values during
installation. Preserve CA issuer/control state when reinstalling the issuer.
Leaving retires cluster credentials/grants but retains node ID and SSH/SOPS keys.

### Execution and source state

| Path | Contents |
| --- | --- |
| `D/receipts/` | Request fingerprints, admission, and results; survives identity reset |
| `D/jobs/` | Authoritative local execution records and retained logs |
| `D/repositories/<id>/` | Fetched base, materialized changes, and pinned Git refs |
| `D/worktrees/<id>/` | Owner-local draft/rebase/preparation worktrees |
| `D/observations/` | Target observations and destructive checkpoints |
| `D/deployments/` | Source/output/generation evidence |
| `D/broker/` | Embedded NATS/JetStream state; quarantine on cluster removal |

Preserve receipts/checkpoints needed to reconcile installation; issuer/single-node
handoff also preserves required control state. Source caches are rebuildable only
while their remote bases and retained increments remain available.

### Other storage

These are outside the base node-install checklist:

- Optional application credentials: external `secretFile` or
  `D/credentials/<credential-id>` on selected holders; independently provisioned,
  never copied wholesale to installers. Cache signing keys use this same store
  and credential lifecycle; there is no separate cache key directory.
- Regenerated runtime values: `R/nats/server.conf`, `R/boot-session.json`, and
  sops-nix outputs under `/run/secrets/`. Preserve their inputs, not these files.
- Temporary input: `R/network/` and durable `D/secret-ingress/<reference>` staging;
  delete after consumption/expiry. Unused join requests also expire.
- Installation handoff: protected `nixmox/handoff/<handoff-id>/` on storage outside
  erase targets; restore preserved values, then delete the consumed bundle.
  `/run` alone cannot preserve a bundle across reboot.

Secret directories/files use `0700`/`0600`, accessible only to the owning service/root;
public policy files remain write-protected. Never place secrets in Git, the Nix
store, JetStream, receipts, logs, or diagnostics. Persist
required paths on ephemeral-root systems; block installation when preservation
cannot be guaranteed. Reusable images contain no node keys. Clones require new
node and machine identities, including SSH/SOPS keys, before simultaneous use.

### SSH-backed SOPS example

```nix
{
  services.openssh.enable = true;
  sops.age = {
    sshKeyPaths = [ "/etc/ssh/ssh_host_ed25519_key" ];
    generateKey = false;
  };
}
```

```sh
ssh-to-age -i /etc/ssh/ssh_host_ed25519_key.pub
# age1... → recipient in .sops.yaml
```

`generateKey = true` creates a separate age key at `sops.age.keyFile`; it is not
SSH-key conversion and is unnecessary here. Restore the SSH key before sops-nix
activation. Verify required secrets decrypt before retiring an old key; removing a
recipient does not revoke access to previously copied ciphertext/plaintext.
[sops-nix](https://github.com/Mic92/sops-nix), [ssh-to-age](https://github.com/Mic92/ssh-to-age)

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
nixmox credentials import cache-signer --kind nix-cache-signing-key --node alder --file /run/keys/cache-key
nixmox credentials import sops-editor --kind sops-age-key --node alder --file /run/keys/age-key
nixmox cache credential set cache-signer
nixmox onboarding sops credential set sops-editor
nixmox credentials list
nixmox repositories credential set lab github-writer
nixmox credentials check github-writer --repository lab --wait
nixmox credentials check cache-signer --wait
nixmox credentials remove unused-token --wait
```

Browser: Settings → Credentials → Add → select storage node → hidden value field
→ Save → Test access. TUI mirrors these controls with hidden input. Module-managed
credentials show their source and cannot be overwritten by UI. Import uses the
same HTTP console as other operations; daemon forwarding to another storage node
uses automated mTLS. No browser/client certificate setup is involved.

Cache signing and SOPS update keys use the same import/storage rules as Git tokens.
Select them in Settings → Cache → Signing credential or Settings → Onboarding →
SOPS update credential, identically in browser/TUI. CLI selection commands above
submit `cache.credential.set` and `onboarding.sops.credential.set`, each with
`credential_id`; these set `cache.signingCredentialRef` and
`prepare.sops.credentialRef`. Nix-declared bindings remain read-only in the UI.
Cache serving requires the selected signing credential on the serving node;
a copy on another node does not enable local serving. See
[cache readiness](cache-gateway.md#serving-readiness-and-cross-host-builds).
The SOPS updater uses its configured age key to authorize recipient changes; host
SSH-derived recipients remain separate. The bundled anchor updater and optional
custom updater follow the [preparation contract](host-onboarding.md#optional-sops-contract).

Import uses the [secret ingress](api-and-interfaces.md#secret-ingress); durable
commands contain only a staging reference. Removal checks repository, cache, and updater bindings.

| Command type | Payload |
| --- | --- |
| credential.import | `name`, `kind`, `storage_node_id`, `staging_ref`; secret ingress supplies this command |
| credential.check | `credential_id`; optional `repository_id`, required for Git kinds |
| credential.remove | `credential_id`; expected credential version; refuse active references or declared ownership |
| cache.credential.set | `credential_id`; expected settings version |
| onboarding.sops.credential.set | `credential_id`; expected settings version |

`credentials check <credential>` dispatches by kind: Git kinds require
`--repository` and test read access without pushing; cache signing keys sign/verify a disposable
challenge; SOPS age keys validate parsing and derive the public recipient; bootstrap
SSH keys validate parsing/signing locally. Non-Git checks reject `--repository`.
Local key checks do not claim target SSH access or repository decryption; those
are checked during bootstrap/preparation. Unsupported kinds return
`UNSUPPORTED_CAPABILITY`. Results include `{kind, checks, scope, passed}` without
secret material. Browser/TUI show the same scope and Remove dependency check.
Removal waits for deletion receipts from every managed replica; unreachable holders
block completion, and provider-side revocation remains separate.
Supported kinds are `git-http-token`, `git-ssh-key`, `ssh-private-key` (bootstrap),
`nix-cache-signing-key`, and `sops-age-key`. Public `scope` lists configured bindings;
import alone creates none. Repository credential selection adds its binding.
Successful Git read checks do not claim write permission; publication checks that
capability through its provider/transport without changing unrelated branches.

Any console can route movable credential-dependent work to an available full member
that holds it; store serving signs locally. If none is available, return
`CREDENTIAL_UNAVAILABLE`; local Git and builds remain usable if source/dependencies
are present. For the first implementation,
provision a second replica independently through a runtime-secret reference or UI
import. This avoids making encrypted cross-node secret distribution a prerequisite.
A replica is not a permanently designated leader.

On leave, clear managed credential copies and revoke membership. Provider-side
rotation is needed for copies on lost/unreachable machines. Nixmox cannot guarantee
erasure on a disconnected host.

## V1 scope

Identity recovery, issuer backup/restore, automatic issuer failover, and cross-node
application-secret replication are outside v1. Planned reinstall must preserve state.
