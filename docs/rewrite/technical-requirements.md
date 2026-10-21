# Technical architecture and acceptance

Implement one domain/service layer consumed by CLI, browser, and TUI. Long-running
operations use the task engine; all durable mutations use NATS/JetStream. Native
protocols handle discovery, remote Git fetches, closure transfer, SSH, and disk I/O.

Apply/rollback use the standard Nix build/rebuild and activation toolchain;
`deployment.install` uses pinned nixos-anywhere/Disko adapters. Shared installer
modules produce media and kexec payloads containing the agent. SSH only bootstraps
temporary agents or serves scoped backend transport; subsequent host orchestration
uses the same NATS protocol. Reject installation-incompatible environments instead
of inventing another fallback or formatting a running system.

## Component contracts

| Component | Owns | Must not own |
| --- | --- | --- |
| Domain | Typed objects, transitions, validation, errors | HTTP, shell execution, rendering |
| Command service | Admission, receipts, version checks, dispatch | Interface-specific behavior |
| Task engine | Execution, resource exclusion, phases, recovery | Form or page state |
| Feature services | Git, deployment, onboarding, membership rules | Duplicate local/remote mutation paths |
| Adapters | Git/Nix/process/device operations | Silent retries of destructive effects |
| Gateway | API, snapshot/event streams, interface assets | Authoritative state in browser sessions |
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
| `nx.<cluster>.log.<task>` | Absolute offset, bytes, owner ID | Core; recover from persisted logs |
| `nx.<cluster>.progress.<task>` | Versioned progress snapshots and activity updates | Core; recover latest persisted snapshot via query |
| `nx_state` KV | Versioned membership, refs, host/task projections | Watch plus snapshot reconciliation |
| `nx_git` Object Store | Retained local Git increments, never remote base snapshots | File-backed JetStream; bounded storage with explicit retention/GC |

Cluster separation also uses NATS accounts/permissions; subject prefixes alone are
not isolation. Pending-node permissions cannot include another node's command
consumer or arbitrary KV/administration access.

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

Enrollment between disconnected brokers uses a narrowly scoped bootstrap bridge.
After association, commands and peer reads use NATS. Topology changes may interrupt
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

Proposed local layout under dataDir:

```text
identity/          private node key, public identity, membership credentials
receipts/          request fingerprints and admission/results
jobs/              authoritative local execution records and log files
repositories/<id>/ fetched remote base, materialized local changes, retained refs
worktrees/<id>/    draft/rebase/preparation working copies
credentials/       private app-managed values; never general replication
observations/      target observations and destructive checkpoints
deployments/       source/output/generation evidence
broker/            embedded NATS/JetStream storage
```

Write authoritative files atomically and durably before acknowledging the effect
boundary they represent. Cluster projections are reconstructable views, not a
replacement for local execution evidence. Logs are append-only bytes with offsets.
Quorum loss blocks new durable commands; already-admitted work follows its effect
policy. No takeover of uncertain target effects based solely on a coordinator lease.

Version every persisted record and wire envelope. Unsupported major versions block
mutation and explain the mismatch; compatible unknown additive fields are retained
where records are rewritten. A migration backs up affected state before replacing
it. **Decision required:** supported mixed-version windows, migration/downgrade
policy, and coordinated backup/restore. System rollback must check daemon-state
compatibility before activating an older service.

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
and stale hosts. IDs belong in logs/traces, not unbounded metric labels.
Redact tokens, private keys, secret command output, and sensitive payload fields.
Exporter failure must not stop tasks; bounded buffers/drop counters are mandatory.

```sh
nixmox diagnostics collect --host alder --wait
```

Browser/TUI: Settings → Diagnostics → select host/time range → Collect. Produce a
reviewable bundle of versions, sanitized configuration, task IDs, connection state,
and bounded diagnostic logs. Exclude credentials and arbitrary repository content.
OTLP export is optional; local structured diagnostics work without a collector.

## Acceptance matrix

| Scenario | Required assertion |
| --- | --- |
| First use | Public remote base plus local changes needs no Git write/SOPS credentials |
| Interface parity | Same form validation, payload, confirmation, and task result in CLI/TUI/browser |
| Three-node deployment | Pinned unpublished source builds on a non-target node and activates on target |
| Apply latest | Host-list/detail action runs build/diff/deploy; default review and explicit auto-approval match CLI `apply --yes`; restart preserves the choice and pinned source |
| CLI defaults | Bare `nixmox apply` targets the local node, follows progress, previews and confirms before switch; remote gateways require an explicit target |
| Stream recovery | Missing/duplicate log chunks reconcile; stale UI is visible |
| Build feedback | Actual cache sources, download rates, active derivations, transfer bars, and unknown totals are correct across interfaces |
| Package diff | Added/removed/version-changed packages compare target and candidate outputs; stale and ambiguous results remain explicit |
| Restart | Command retry resolves the same task; effects are not blindly replayed |
| Quorum loss/recovery | Writes block without unsafe fallback; readable stale data stays labeled |
| Removal/reuse | Identity retained, membership revoked, no stale routes/old broker identity reused |
| Installer handoff | Same node returns on changed endpoint with verified deployed configuration |
| Fresh install/recovery | kexec and normal reboot enter a verified installer before destructive review; Pi SD recovery survives NVMe reinstall; identity/network handoff and single-node state survive |
| Operation boundaries | One Install command handles first/fresh installs; Apply never formats; unsupported entry returns INSTALLATION_UNSUPPORTED before effects |
| Agent entry parity | USB/SD, kexec, installed agent, and SSH bootstrap converge on the same identity/task protocol; bootstrap requires no preinstalled Nix |
| Installer bootstrap | Auto-launched TUI, Wi-Fi setup, discovery timeout/retry, and headless Ethernet enrollment share browser/TUI state; LAN access works without internet |
| Installer profiles | Pinned x86/Pi/custom recipes prefer the capable writing host for builds; remote overrides/fallback transfer verified images; USB/SD writing is architecture-independent; advertised board/boot support has physical test evidence |
| Git durability | Another node fetches the remote base and applies retained changes after writer loss |
| Git retention | No full repo in JetStream; published inclusion or reviewed deletion controls change cleanup |
| Security boundary | Pending node cannot read credentials or command another node |

For application changes, extend `e2e/run.py` and run both parts of comprehensive
three-node E2E. Test the actual changed source with working-tree snapshot mode,
or an authorized published revision. Never publish merely to run tests. Report
source mode, tested SHA, final checkpoint, artifact directory, and failure evidence.
Unit/schema/WASM/browser checks complement this gate. Destructive hardware claims
also need disposable-device evidence; VM assertions are not physical USB validation.
