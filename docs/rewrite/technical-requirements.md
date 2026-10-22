# Technical architecture and acceptance

Implement one domain/service layer consumed by CLI, browser, and TUI. Long-running
operations use the task engine; cluster-scoped durable mutations use NATS/JetStream.
The explicit [node-local operations](api-and-interfaces.md#operation-registry-and-scope)
reuse durable local receipt/task storage before enrollment and during quorum loss. Native
protocols handle discovery, remote Git fetches, closure transfer, SSH, and disk I/O.

Backend ownership: [deployment](deployment-and-rollback.md#execution-contract),
[installer execution](host-onboarding.md#installation-backend-and-handoff), and
[cache transfer](cache-gateway.md). NATS carries control/progress; native protocols
carry Git, SSH bootstrap, and Nix output bytes.

## Component contracts

| Component | Owns | Must not own |
| --- | --- | --- |
| Domain | Typed objects, transitions, validation, errors | HTTP, shell execution, rendering |
| Command service | Admission, receipts, version checks, dispatch | Interface-specific behavior |
| Task engine | Execution, resource exclusion, phases, recovery | Form or page state |
| Feature services | Git, deployment, onboarding, membership rules | Duplicate local/remote mutation paths |
| Adapters | Git/Nix/process/device operations | Silent retries of destructive effects |
| Gateway | API, snapshot/event streams, interface assets, cache routing | Authoritative state in browser sessions or package archive storage |
| Presentation core | Forms, action availability, state formatting | Secrets or authoritative authorization |

Proposed implementation language: Go for daemon/CLI/TUI and a pure Go presentation
package compiled to browser WASM. The native TUI links the same package. Browser
DOM and terminal rendering are adapters. Confirm the compiler/runtime footprint
with a vertical slice before adopting framework dependencies.

```text
Browser → WASM presentation ─┐
TUI → native presentation ───┼→ typed API → durable command → task → adapter
CLI → typed arguments ───────┘       ↑                         │
                             snapshots/events ← NATS ← persisted state/logs
```

Example presentation boundary, expressed as Go-like interfaces:

```go
type Review struct {
    Fields []FieldView
    Actions []ActionView // enabled plus reason, never merely hidden
    Warnings []string
}
type Presentation interface {
    RenderForm(schema FormSchema, values Values, errors []FieldError) Review
    HostActions(host HostSnapshot, capabilities Capabilities) []ActionView
    TaskProgress(task TaskSnapshot) ProgressView
}
```

The service repeats validation at execution. A stale UI cannot authorize an action
by hiding its disabled state. Test identical input fixtures through native and WASM
presentations for identical field errors/actions; rendering need not be identical.

### TUI implementation

Use Charm's [Bubble Tea](https://github.com/charmbracelet/bubbletea) for interaction,
[Bubbles](https://github.com/charmbracelet/bubbles) for reusable controls, and
[Lip Gloss](https://github.com/charmbracelet/lipgloss) for styling/layout. Pin compatible
versions. Keep these dependencies in the terminal adapter, outside the shared core.
Provide a consistent theme, clear focus/action hints, keyboard-complete navigation,
responsive layouts for small/resized terminals, and scrollable diffs/logs. Live
progress must preserve focus and scroll position; status must remain understandable
without color. Use the same validation, reviews, and task data as the browser.

## NATS namespace and durability

All interpolated IDs use a canonical subject-safe encoding; no raw hostname/ref.
The following names are proposed protocol constants.
Listener defaults and conflict handling are defined in
[runtime service ports](runtime-configuration.md#service-ports). Embedded NATS
must use those configured ports rather than its conventional defaults.

| Subject or store | Payload | Delivery |
| --- | --- | --- |
| `nx.<cluster>.command.<node>` | Command ID/type/target, expiry, payload, trace context | JetStream, explicit ack, per-executor durable consumer |
| `nx.<cluster>.query.<node>` | Request ID, typed query, chunked reply address | Core request/reply, bounded timeout |
| `nx.<cluster>.activity.<node>` | Replaceable resource counters | Core, no replay |
| `nx.<cluster>.result.<node>` | Command/task ID, executor, versioned outcome | JetStream, deduped by task/command receipt |
| `nx.<cluster>.log.<task>` | Absolute offset, bytes, owner ID | Core; recover from persisted logs |
| `nx.<cluster>.progress.<task>` | Versioned progress snapshots and activity updates | Core; recover latest persisted snapshot via query |
| `nx_state` KV | Versioned membership, refs, host/task projections | Watch plus snapshot reconciliation |
| `nx_git` Object Store | Retained local Git increments, never remote base snapshots | File-backed JetStream; bounded storage with explicit retention/GC |

Enforce [machine permissions](identity-and-trust.md#managed-tls-and-nats-permissions)
through NATS accounts and accepting-broker grants, not subject prefixes alone.
Leaves stream assigned work without becoming voting replicas.

Commands expire after 120 seconds if not admitted. Broker deduplication is an
optimization; durable request fingerprints/receipts enforce retry identity.
Use discard-new backpressure on full queues. Error outcomes must distinguish
rejected before admission from unknown after submission.

Proposed initial bounds: command payload 256 KiB; query chunk 64 KiB; total bounded
query 64 MiB; log event 64 KiB. Larger Git artifacts use chunked object storage,
not oversized command payloads. UI event cadence: coalesce state changes up to
250 ms, activity every 2 seconds, inventory every 30 seconds, heartbeat every
10 seconds. Mark stale after 20 seconds without a heartbeat, offline after 35.
Thresholds are protocol/config constants with tests, not duplicated UI timers.

Subscribe before reading a snapshot/log tail, then merge by resource version or
byte offset. Reconnection starts a fresh snapshot; safety refresh catches missed
final updates. Telemetry never renews membership by itself. A disconnected browser
freezes uptime rather than pretending the source is still live.

Enrollment uses a limited TLS bootstrap exchange on the machine listener outside NATS; unapproved nodes
have no cluster broker connection. After cluster approval/Add, commands and control
queries use authenticated NATS. Topology changes may interrupt
the transport carrying a result; look up the durable receipt, never rerun effects
through an unqueued fallback.

## Nix progress adapter

Consume machine-readable Nix activity output at the executing node and normalize
it into task progress; do not scrape ANSI progress bars from NH/nom. Nix exposes
`--log-format internal-json`, which is the proposed input, with supported versions
and activity types tested explicitly rather than assumed to be a stable wire API.
[Nix logging formats](https://nix.dev/manual/nix/2.25/command-ref/new-cli/nix3-log)

Collect download/cache activity, byte counters, derivation builds, and remote
executor information from actual events; supplement with bounded plan/store queries
when needed. Capture target-copy and activation activities from their own adapters.
Unsupported fields/events produce an explicit unavailable/degraded telemetry state
and retain sanitized logs; they must not crash or alter the underlying task outcome.
Do not infer completed work from text fragments or terminal cursor movement.

Use the shared presentation core for percentage/rate/unknown-state formatting.
Coalesce progress up to 250 ms while delivering terminal/failure transitions
promptly. Bound active previews and paginate detailed lists. Test adapter fixtures
for supported Nix versions plus actual cache download/build/copy operations in E2E.

## Package diff adapter

Package comparison adapters normalize closure metadata into the structured
[package diff](deployment-and-rollback.md#required-package-diff). Preserve both
output identities, versions, and ambiguous matches; do not parse colored terminal
tables. A diff backend is an implementation detail; all interfaces consume one
schema and share category/count formatting through the presentation core.

## Persistent state

Use the authoritative [storage layout](identity-and-trust.md#generated-and-preserved-state)
for identity, credentials, execution records, Git data, and broker state.

Write authoritative files atomically and durably before acknowledging the effect
boundary they represent. Cluster projections are reconstructable views, not a
replacement for local execution evidence. Logs are append-only bytes with offsets.
Quorum loss blocks new cluster-scoped durable commands; already-admitted work follows its effect
policy. No takeover of uncertain target effects based solely on a coordinator lease.

Version persisted records and wire envelopes for identification. Mixed-version
operation, state migration/downgrade orchestration, and coordinated backup/restore
are outside v1 scope; no compatibility guarantee is implied by system rollback.

## OpenTelemetry and diagnostics

```json
{
  "service.name": "nixmox",
  "nixmox.node_id": "n_alder",
  "nixmox.cluster_id": "c_lab",
  "nixmox.command_id": "cmd_apply_cedar_01",
  "nixmox.task_id": "task_01",
  "event": "deployment.phase",
  "phase": "transferring"
}
```

Propagate standard trace context through command/query envelopes and child tasks.
Example span chain: `command.admit → task.execute → git.materialize → nix.build →
closure.transfer → deployment.activate → deployment.observe`. Async work links to
its originating command; traces do not determine task success.

Metrics: command admission latency, queue depth, phase duration, failures by error
code, broker reconnects, Git bytes transferred, artifact storage failures, log catch-up,
cache lookup latency, streamed bytes, peer failures, and stale hosts. IDs belong
in logs/traces, not unbounded metric labels.
Redact tokens, private keys, secret command output, and sensitive payload fields.
Exporter failure must not stop tasks; bounded buffers/drop counters are mandatory.

```sh
nixmox diagnostics collect --host alder --wait
```

Browser/TUI: Settings → Diagnostics → select host/time range → Collect. Produce a
reviewable bundle of versions, sanitized configuration, task IDs, connection state,
and bounded diagnostic logs. Exclude credentials and arbitrary repository content.
OTLP export is optional; local structured diagnostics work without a collector.

`diagnostics collect --host <host>` aliases `hosts diagnostics <host>` and submits
`host.diagnostics`. Optional `--since`/`--until` map to UTC timestamps; `until`
defaults to admission time and `since` to `until` minus one hour. Reject reversed
ranges or spans over 24 hours.
The task returns `diagnostic_id` and `download_url`. Reads:
`GET /api/v1/diagnostics/{id}` returns
`{id, version, host_id, owner_node_id, since, until, bytes, sha256, state, expires_at, download_url}`;
`GET /api/v1/diagnostics/{id}/download` streams the sanitized tar.gz bundle with
filename, digest, and length. Retain for 24 hours after completion; expiry returns
410 and unavailable owners return 503. Stream directly over HTTP, never JetStream.

## Acceptance matrix

Run shared contract fixtures across CLI/TUI/browser, plus three-node E2E for
distributed behavior. Native/WASM checks must agree on validation and actions.
Physical boot/media claims require disposable-device evidence; VMs alone are insufficient.

| Area | Required checks |
| --- | --- |
| Configuration | Generate without host/write credentials; template selection/pinning; safe serialization/paths; incomplete commits; hardware with/without filesystems; generated/bound multi-disk layouts; preserve manual edits |
| Interface parity | Same operations, defaults, validation, reviews, errors, and results; explicit unknown/stale state; every verb maps to a registered operation/read |
| Reviews | Start without advance erase approval; reject stale bindings/missing acknowledgements; unrelated health/progress does not invalidate approval; duplicate resume cannot repeat effects |
| Reads | Pagination/filter bounds, revision-pinned catalogs/files, log gaps/per-build logs, download expiry and unavailable owners |
| Admission/recovery | Interrupt every admission boundary; retry resolves the same task; restart queued/running/cancelling work; uncertain effects reconcile without destructive replay |
| Local scope/quorum | Network/discovery/join intent survive restart without quorum; cluster mutations block; stale reads stay labeled; no fallback or lease-only takeover |
| Enrollment | Create with no full members; approve/reject/expire and cluster Add; conflicting keys/clusters; promotion, removal, fresh rejoin, authenticated removal notices |
| Trust/secrets | Deny anonymous broker access, unapproved nodes, pending route peers, and unauthorized leaf subjects; rotation/removal reconcile grants; preserve issuer; no secrets in Git, commands, logs, or diagnostics |
| Transport | HTTP console/secret submission without certificates; machine-only routes absent there; managed backend TLS; custom ports, firewall, reservations, occupied listeners |
| Installer bootstrap | Linux SSH bootstrap without Nix; privileges/host keys/cleanup; automatic TUI, headless Ethernet, Wi-Fi hotplug/errors, discovery timeout/retry/late results, joining from either side |
| Installation | USB/SD, kexec, and reboot converge; incompatible entry fails before effects; media/boot/erase approvals separate; same identity/output returns; single-node/issuer state survives; no repeated partitioning |
| Preparation/SOPS | Reuse unassigned configuration; reject swapped/duplicate disks; confirm every erase target; no-SOPS install; updater failures; exact rule selection, all key groups, shared aliases, idempotent recipient edits |
| Media/recovery | Physical profile validation, compressed image capacity/readback, writer preference/emulation/overrides, remote transfer failure; recovery survives Apply/rollback/GC |
| Apply | Local/default/remote-target rules; unpublished remote build; pinned source through rebase; manual/auto approval and restart; changed baseline; boot staging; activation interruption and target exclusion |
| Rollback/health | Retained/missing outputs; desired/running drift; exact output verification and separately reported health; no automatic rollback |
| Diff/progress | Added/removed/version/output/ambiguous changes; target baseline; changing/unknown totals, cache sources/rates, retries, simultaneous builds/downloads, reconnect; byte completion never implies deployment success |
| Cache | Split-peer closures; bounded streaming/failure; signatures, pins/GC, cancellation, pending access; missing/invalid signer keeps serving offline and gateway responsive; config/runtime credential activation; cross-host build gating and key-loss recovery |
| Git | Independent repositories; writer loss; missing base/artifact; concurrent edits and upload/CAS restart; conflict abort; pinned old commits; PR push versus upstream inclusion; reviewed deletion; no full base in JetStream |
