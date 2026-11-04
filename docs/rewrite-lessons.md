# Behaviors and lessons for a Nixmox rewrite

This records application behavior absent or underspecified in [requirements.md](requirements.md) for use when planning a rewrite. It distinguishes useful contracts from current implementation choices and defects. Preserve useful observable contracts; replace the mechanisms where they are duplicated, incomplete, or unreliable.

Snapshot: 2026-11-04, working tree based on `53ab6d5`, including uncommitted membership work. Requirements SHA-256: `1bde08f53b609e01b6e865598a6b1bfe8e23893a393e86d5edb19a1e64c8305b`. Source links refer to that working tree and will change as implementation changes. This review covers the main backend workflows, client streaming, console data handling, and their tests; it is not an exhaustive inventory of visual UI behavior or every configuration default.

Each item distinguishes a detail absent or underspecified in requirements from an existing requirement, an implementation choice, or a defect. Proposed rewrite contracts are recommendations for review, not newly approved requirements.

## Durable tasks and ownership

### Resource locks do not expire when a host disconnects

Resources are stored with native KV compare-and-swap, grouped by host or cluster scope. Acquisition sorts scopes, starts the runner only after all resources are acquired, and rolls back partial acquisition. Locks have no TTL and are not stolen because a client disappears.

**Underspecified in requirements:** concurrency protection is required, but lock lifetime and abandoned-lock recovery are not fully defined. Preserve the principle that loss of contact is not proof that a mutating process stopped; explicitly design recovery of abandoned locks.

Source: [resources.go](../internal/task/resources.go), `Acquire`, `Release`.

### Task and child identities carry ownership and deduplication

Task IDs are host-qualified. The scheduler rejects records whose key and embedded identity disagree, preventing a writer with a host-scoped subject from claiming work for another host. A child task ID is deterministic from parent ID and child role; re-submission checks that its inputs and concurrency contract agree rather than creating another child.

**Preserve:** stable child identity across lost replies/restarts, and enforcement of the owner in both storage addressing and record contents. The UUID algorithm and key formatting are implementation choices.

Source: [manager.go](../internal/task/manager.go), `SubmitChild`, `submit`, `each`.

### Retry and resume have different semantics

A manual retry creates a new task and resets execution context, result, review, and child links. Resumption retains the original persistent task/checkpoints. Local snapshots are recovery material, not another scheduler; startup reconciles from shared task records. Snapshot filenames validate the host-qualified UUID.

**Rewrite lesson:** an irreversible operation must not rely on retry inheriting the old checkpoint. The current removal code can need original member evidence after deleting the member record, so manual retry and crash-resume must both be tested explicitly. Copying every checkpoint to every retry is not a general solution either; stale approvals must not carry forward accidentally.

Source: [manager.go](../internal/task/manager.go), `Retry`, `submit`, `reconcile`; [journal.go](../internal/task/journal.go); [removal.go](../internal/membership/removal.go).

### Cancellation and approval are ordered around side effects

Cancellation requests stop the active runner; completion releases resources afterward. Process cancellation kills the process group, not just the immediate executable. A review output is persisted before the task becomes reviewable; approval identifies the exact output and rejects stale approvals. The runner has returned before the manager exposes manual-review state.

**Preserve:** do not release a host lock while its cancelled subprocess can still mutate the host. Approval must apply to a specific immutable review. Requirements describe cancellation and review but not this ordering.

Source: [manager.go](../internal/task/manager.go), `Cancel`, `Approve`, `execute`; [execution.go](../internal/task/execution.go), `Review`; [process.go](../internal/host/process.go).

### Parent and child tasks must not deadlock over the same host

After installed-boot validation, the installation task checkpoints that fact and releases its host resource before requesting a coordinator-owned promotion child that needs that resource. It must not continue host mutation after yielding ownership.

**Preserve:** explicit ownership handoff between task phases. A generic parent-waits-for-child abstraction alone does not solve shared-resource deadlocks.

Source: [execution.go](../internal/task/execution.go), `ReleaseResources`; [deployment/install.go](../internal/deployment/install.go), `InstallRunner`.

### Request lifetime is not operation lifetime

Cluster startup after saving initial state and accepted durable tasks use daemon-owned lifetimes. Tracing context is propagated through requests and stored with tasks without preserving the submitting HTTP request's cancellation. Completion persistence uses a bounded context independent of an already-cancelled runner.

Scheduler transient-read retries do not retry mutations or entire task runners. Membership RPCs retry no-responders, but not ambiguous reply timeouts. Remote error responses currently flatten errors into strings, losing typed error information.

**Preserve:** a timeout means the result may be unknown; it does not prove that a mutation did not occur. Define idempotency keys and reconciliation for mutations. **Replace:** string-only remote errors if callers need to distinguish validation, availability, and uncertain completion.

Source: [transport.go](../internal/cluster/transport.go), `DispatchContext`, `ResumeContext`, `RequestJSON`; [membership.go](../internal/daemon/membership.go), `applyRemoteMembership`; [manager.go](../internal/task/manager.go), `schedule`, `execute`.

## Identity and trust

### Existing identity must fail closed

One process holds an exclusive identity-directory lock. The private key is persisted before initial node state, allowing an interrupted first start to reuse the key. Corrupt state, a missing key for existing state, or an invalid key is an error; it does not trigger replacement identity. Private writes use a temporary file, file sync, rename, and directory sync. Files are private and directory ownership is treated as a trust boundary.

**Preserve:** never turn corruption or incomplete restoration into an apparently new machine. Atomic replacement of one file does not make a sequence of identity/topology writes atomic.

Source: [identity.go](../internal/identity/identity.go), `Open`, `WritePrivate`, `WritePrivateAt`.

### System management uses a separate identity

Ordinary application membership credentials do not directly grant SYS metadata administration. Full members can create a short-lived-in-use management connection with an ephemeral key and a cluster-signed `system:<nodeid>` identity. SYS permissions are scoped to evacuation/removal and the identity's reply inbox. Role-based account reloads remove that management principal when full membership ends.

**Preserve the privilege boundary:** separate application access from peer-management operations. Do not preserve the current dynamic reload implementation without resolving its native concurrency issues. The requirements' broad TLS/authorization statements do not specify these distinct principals.

Source: [authorization.go](../internal/cluster/authorization.go), `SystemIdentity`, `ConfigureAccounts`; [daemon/authorization.go](../internal/daemon/authorization.go), `systemConnection`; [authorization_test.go](../internal/daemon/authorization_test.go).

### SSH identity and TLS identity serve different purposes

Age identity is derived in-process using the upstream SSH-to-age implementation from the SSH host key, not the TLS key. SOPS runs with an explicit private identity file and isolated configuration/home environment. Secret subprocess arguments and stdin are not logged. Errors are sanitized rather than returning tool output that could expose secret material.

**Preserve:** deterministic compatible derivation for copied bootstrap binaries without requiring an SSH-to-age executable. The requirements already specify SSH-derived age keys; helper-free operation and ambient-environment isolation are additional implementation contracts. Removing a recipient re-encrypts current documents; it cannot erase plaintext or old ciphertext already copied by that member.

Source: [cipher.go](../internal/credentials/cipher.go); [process.go](../internal/host/process.go).

## Storage, events, and external interfaces

### Storage revisions and watch initialization are observable

Store update/delete operations require a nonzero expected revision. Listing uses a sorted key cursor, not a snapshot, so concurrent mutation can affect later pages. A KV watch forwards the initial-snapshot completion marker as a nil record; deletion notifications have keys/revisions without decoded values. Unexpected watch closure and callback errors terminate the wrapper.

**Preserve or explicitly redesign:** initialization, deletion, cursor, and revision-conflict semantics. A subscriber must not interpret the initial completion marker as a new member. Requirements say to use subscriptions but do not describe these event contracts.

Source: [store.go](../internal/cluster/store.go), `Update`, `Delete`, `List`, `Watch`; [http.go](../internal/api/http.go), `respondRecord`.

### A disconnected member does not become an unjoined discovery agent

Unjoined discovery uses bounded local memory. Joined discovery uses shared storage and clears the local cache. A disconnected joined node reports shared-store unavailability instead of falling back to local observations as though it were unjoined. Duplicate advertisements are write-throttled.

**Preserve:** membership state controls storage authority, not current connectivity. This complements the explicit membership/reachability rule but is not stated for discovery in requirements.

Source: [discovery/service.go](../internal/discovery/service.go), `storage`, `Observe`.

### SSE and log cursors acknowledge successful consumption

The SSE client advances its cursor after the event callback succeeds and reconnects reads using `Last-Event-ID`. Callback/protocol errors stop the subscription; retryable transport failures reconnect. Log replay and live follow use stream sequence numbers. A history page fixes its ending sequence when the read starts, rather than chasing an indefinitely growing log.

**Preserve:** no gaps at history-to-live handoff and no cursor advance before successful consumption. Consumers still need an explicit duplicate-handling policy when their callback has partial effects before returning an error.

Source: [stream.go](../internal/client/stream.go), `Watch`, `readEvents`; [logs.go](../internal/cluster/logs.go), `Read`, `Follow`; [logs_test.go](../internal/cluster/logs_test.go).

### Cache failover ends when response streaming begins

The cache gateway tries another peer before committing a response. After streaming begins, a failed source aborts the response instead of appending another source's bytes. It does not follow redirects. All-peer not-found, peer unavailability, and an empty peer set have different HTTP results. Peer snapshots replace atomically.

**Preserve:** never splice artifact bytes across peers. Requirements describe failover but not this boundary or the difference between absence and unavailability.

Source: [gateway.go](../internal/cache/gateway.go), `ServeHTTP`, `ReplacePeers`.

### Interfaces distinguish unknown submission from rejection

The HTTP client returns a dedicated `UnknownSubmission` error when a mutation encounters a transport failure, unreadable or oversized successful response, or invalid response JSON. It does not automatically repeat the mutation. A received non-success HTTP response is represented separately with status and message. Requests are confined to the selected daemon endpoint.

**Preserve:** interfaces must not present an uncertain result as a definite rejection or encourage an automatic duplicate submission. This is a user-visible behavior, not just a transport implementation detail.

Source: [client.go](../internal/client/client.go), `Do`, `incomplete`, `request`.

### Partial console refresh failures do not discard all data

After reading health and cluster state, the console fetches hosts, tasks, repositories, recipes, and configurations concurrently. Individual collection failures become named warnings alongside successful collections. Pagination rejects a repeated cursor and bounds the total collection size. The selection queue keeps the latest selection; the command queue reports when it is full instead of silently accepting unlimited commands.

**Implementation choices worth reviewing:** preserve the distinction between partial data availability and total disconnection. These are current interface behaviors absent from the requirements, not reasons to keep the current UI architecture.

Source: [connection.go](../internal/ui/views/connection.go), `snapshot`, `readCollection`, `Select`, `Submit`.

### Existing validation and limits are choices to review

HTTP request decoding rejects unknown fields and extra JSON values. The API limits request bodies to 1 MiB; ordinary list pages default to 100 and cap at 500. SSE events are bounded to 1 MiB, membership RPC outputs to 512 KiB, credentials to 64 KiB, captured process stdout to 8 MiB, and discovery datagrams to 8192 bytes.

**Implementation choices:** preserve boundedness, but do not automatically freeze these numeric values into a new product specification. Define failure behavior and compatibility before changing them. Strict unknown-field rejection affects rolling upgrades with mixed daemon versions.

Source: [server.go](../internal/api/server.go); [http.go](../internal/api/http.go); [stream.go](../internal/client/stream.go); [observations.go](../internal/daemon/observations.go); [cipher.go](../internal/credentials/cipher.go); [process.go](../internal/host/process.go); [discovery/model.go](../internal/discovery/model.go).

## Configuration and installation

### Template values retain types and are not evaluated twice

A whole-value template expression preserves its result type, including lists, maps, and booleans. An expression mixed with surrounding text becomes a string. Missing template keys are errors. Text supplied through a field is inserted literally rather than reparsed as another template.

**Additional contract:** the requirements name Go interpolation but do not define these type and evaluation rules. Preserve them so selection lists remain lists, booleans do not become strings, and user text containing template syntax is not unexpectedly executed.

Source: [template.go](../internal/recipe/template.go), `Evaluate`, `Text`.

### Invalid new input and stale existing input are treated differently

New submissions are explicitly validated and rejected when invalid. Previously collected fields and observations that become stale are removed in dependency order, allowing invalidation to propagate. Selection matching uses typed equality rather than string coercion. A missing dependency cannot be satisfied merely by submitting a dependent field.

**Additional contract:** requirements already describe invalidation; preserve the distinction between rejecting a bad submission and invalidating an old value. Do not silently discard invalid newly submitted input and report success. Typed selection equality is a compatibility choice to review.

Source: [inputs.go](../internal/configuration/inputs.go), `validateSubmitted`, `invalidateStaleInputs`; [fields.go](../internal/recipe/fields.go), `Validate`.

### Rendered output collisions fail before writes begin

All output patterns are evaluated and normalized before files are written. Two different patterns resolving to the same cleaned path cause an error. Output content is collected and bounded before the write phase. Writes still are not an atomic transaction: an I/O failure during writing requires discarding the private worktree.

**Preserve:** detect collisions instead of silently letting one generated file overwrite another. The requirements specify output generation but not this failure behavior.

Source: [render.go](../internal/recipe/render.go), `Render`.

### Repository registration has stable configuration identity

Discovered configuration IDs are derived from repository ID, flake directory, and configuration name. Repeated registration returns the existing latest configuration rather than duplicating it or silently advancing its pin. Explicit refresh checks that the named configuration still exists at the new revision. Nix enumeration forbids writing or updating the lock file.

**Additional contract:** stable registration identity and removal-at-new-revision failure. Do not turn refreshing a repository into an implicit deployment change. The precise UUID derivation is replaceable if identity stability is preserved.

Source: [registry.go](../internal/configuration/registry.go), `Register`, `Refresh`, `names`.

### Activation rechecks assignment after review

Deployment checks the host's configuration assignment before building and again after approval, requires exactly one NixOS system output, roots that output against garbage collection, records previous/proposed paths, changes the system profile, runs activation, and verifies that the running system actually points at the proposed path. The reported generation is read from the system profile link.

**Preserve:** an approval does not authorize deployment of a configuration that has since been unassigned. A successful activation command alone is not sufficient evidence of the expected running system. Profile update and activation are separate side effects; a rewrite must account for failure between them without inventing automatic rollback, which requirements exclude.

Source: [activate.go](../internal/deployment/activate.go), `Runner`.

### Pinned inputs must fail rather than silently change

Git pins require a full lowercase 40-character commit SHA, repository identity, and reference. Missing pinned commits fail checkout. URLs reject embedded passwords and inappropriate embedded credentials/query fragments. Reads beneath worktrees use pinned filesystem roots so symlinks cannot escape. Preview and deployment materialization share rendering; preview discards its worktree and does not collect new observations or publish.

**Already required in principle:** immutable pins and isolated rendering. **Additional contracts:** strict pin validation, no fallback to a newer commit, confined file access, and shared preview/materialization semantics. Exact SHA format is a compatibility choice rather than a timeless Git constraint.

Source: [source.go](../internal/source/source.go); [configuration/render.go](../internal/configuration/render.go); [recipe/render.go](../internal/recipe/render.go).

### Remote observation failures must not observe the coordinator instead

An observation targets the requested host. Unavailable remote operations fail rather than falling back to inspecting the local host. This is essential for disks, hardware configuration, and SSH-derived keys: successful collection from the wrong machine is worse than a visible failure.

**Preserve explicitly.** Source: [collector.go](../internal/configuration/collector.go), `Collect`.

### Installer compatibility and executable readiness are separate

Explicit installer/prepare choices filter candidates but cannot override hard compatibility failures. Compatible candidates are ranked by combined preference, with installer ID as a deterministic tie-breaker. An otherwise compatible selection can still report that execution is unavailable because transition machinery is missing.

**Preserve:** manual selection does not bypass hardware safety, and a matching definition is not proof that a launch workflow is implemented. The specific supported prepare method set is incomplete and should not be mistaken for the full requirements.

Source: [checks.go](../internal/installer/checks.go), `Select`, `Checks`, `ready`.

### Interrupted formatting is not automatically repeated

Installation checkpoints disk preparation before running the destructive script and checkpoints completion afterward. If preparation started but completion was not saved, resumption refuses automatic reformat and requires inspection. After preparation completed, resumption runs the mount script instead. Built installation artifacts are rooted against garbage collection before use.

**Preserve:** treat interruption around destructive side effects as uncertain completion, not permission to repeat them. Generic persistent-task support does not supply this behavior automatically.

Source: [deployment/install.go](../internal/deployment/install.go), `InstallRunner`.

### Installation validation binds more than connectivity

Preservation records bind node/cluster/task IDs, old boot ID, expected system/generation, destination locations, and a hash of the SSH host key. Installed validation compares the preserved manifest with the task contract. Writes target a verified installation root using confined private-file operations. The code carries supported NetworkManager, iwd, and wpa_supplicant configuration files, not merely a Wi-Fi network name.

**Already required in principle:** identity/network preservation and boot validation. **Additional detail worth retaining:** precise identity and destination binding, symlink-safe destination handling, and refusal to accept a changed preservation contract.

Source: [preservation.go](../internal/daemon/preservation.go); [installer/preservation.go](../internal/installer/preservation.go); [identity.go](../internal/identity/identity.go).

### Automatic promotion verifies installation evidence

Beyond requiring a successful deployment, the coordinator verifies a running persistent fresh-deployment task owned by the target, its recorded installation validation, released host resources, unchanged configuration assignment, installed environment, changed boot ID, expected system path, and expected generation. Promotion certificates are checked against the original enrolled public-key thumbprint.

**Preserve:** evidence must bind the observed installation to the same task, host, identity, and configuration. A status string supplied by the target is insufficient.

Source: [service.go](../internal/membership/service.go), `validateDeployment`; [membership.go](../internal/daemon/membership.go), `promotionCertificate`.

## Small NATS implementation lessons not covered by ownership policy

The ownership algorithm is already documented in requirements and is intentionally not repeated here. Keep these distinct operational lessons:

- Launch mutually dependent embedded peers concurrently before waiting for readiness, including cold restoration.
- Stopping a process does not remove its persisted peer membership; permanent removal is a separate operation.
- Restart preserves an incarnation and storage. Recreation after retirement needs a new incarnation; changing only the storage directory did not make immediate same-name recreation work in the recorded experiment.
- Listener readiness, metadata readiness, replica availability, and completed placement changes are separate observations. The current transition code is not validated as reliable.

Source: [transport.go](../internal/cluster/transport.go), [startup.go](../internal/daemon/startup.go), [peer_lifecycle_test.go](../internal/daemon/peer_lifecycle_test.go).

## Defects and pitfalls to avoid in a rewrite

- A fixture that directly calls an internal orchestration function does not validate public API/event wiring. The membership fixture does this today; use it as a testing lesson, not an architecture template.
- Multi-record operations and local files do not commit atomically. Define authoritative state, transition intent, projections, and resumable steps instead of letting all copies appear equally authoritative.
- Host-specific observations must never silently run on the coordinator when a remote operation is unavailable.
- Remote mutation errors are flattened to strings; callers lose distinctions needed for validation, availability, and uncertain outcomes.
- A passing compatibility check or CLI command definition does not prove the execution workflow exists. Installer transitions, some mutation routes, and recovery paths remain incomplete.
- A single passing integration run is not acceptance evidence for an intermittent workflow. Later regression runs reproduced a retirement timeout; its cause has not been established.
- Race testing found native NATS options reload racing with direct stream reads and route reconnect handling. That is separate evidence from the timeout; no causal link has been demonstrated.

Sources: [removal_test.go](../internal/daemon/removal_test.go), [collector.go](../internal/configuration/collector.go), [transport.go](../internal/cluster/transport.go), [checks.go](../internal/installer/checks.go), and [PROGRESS.md](../PROGRESS.md). Historical diagnostic logs were `/tmp/nixmox-removal-full.log` and `/tmp/nixmox-removal-race.log`; temporary logs are not durable acceptance artifacts.

## Rewrite acceptance examples worth retaining

These are proposed scenarios, not claims that the existing suite covers all of them.

- Drive state changes through their real API/event path; do not invoke the orchestration function from fixtures to make the expected outcome happen.
- Re-register an unchanged repository; verify identity and pins remain stable. Refresh to a commit without the selected configuration; require a visible failure.
- Supply a list-valued template, a boolean, literal template-looking user text, a missing key, and colliding output paths; verify the documented types and failure boundaries.
- Change configuration assignment while deployment awaits approval; verify stale work cannot activate.
- Cancel a subprocess with children; a conflicting task must not start while those children can still mutate the host.
- Lose a mutation reply; prove that retry cannot execute the side effect twice. Separately prove deterministic child submission after a lost reply.
- Interrupt disk preparation; prove that resumption does not automatically format again.
- Disconnect an artifact source mid-response; prove that no second peer's bytes are appended.
- Disconnect/reconnect log delivery at the replay/live boundary; verify cursor handling without gaps.
- Run authorization changes under concurrent reads, route reconnects, and peer removal with the race detector enabled.
