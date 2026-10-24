# Technical architecture and acceptance

CLI, browser, and TUI share one domain/service layer and task engine. Cluster
mutations use JetStream; [node-local operations](api-and-interfaces.md#operation-registry-and-scope)
reuse local receipts/tasks before enrollment and during quorum loss.

## Communication boundaries

NATS carries inter-node commands, queries, state, logs, telemetry, Git increments,
and secret delivery. These paths stay outside NATS:

| Communication | Transport |
| --- | --- |
| Console API, forms, SSE, assets, uploads, log/media/diagnostic downloads | HTTP |
| Initial CSR/certificate enrollment and signed removal retrieval | Same HTTP port as the console |
| Nix cache lookup/downloads | HTTP; Nix verifies signatures/hashes |
| LAN discovery | UDP |
| Initial agent bootstrap | SSH |
| Remote Git fetch/push, PR APIs, release images, external Nix caches | Provider's native HTTP(S)/SSH transport |
| Optional telemetry export | Configured OTLP transport |
| Local tools, Nix daemon, network backend, devices | Process/OS IPC and I/O |

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

Use Go for the daemon/CLI/TUI and shared presentation/template logic.

| Interface | Rendering and updates |
| --- | --- |
| Browser | [go-app v11](https://go-app.dev/) components compiled to WASM; shared Go controllers/view models, thin DOM adapters |
| TUI | Native shared Go logic + Charm, including live configuration preview/editing |
| CLI | Typed arguments and JSON API; shared events for watching tasks |

Follow `internal/ui/components/` and `internal/ui/views/layout.go`: renderer-independent Go component controllers,
explicit actions and complete view models, with thin native/terminal and go-app/DOM
adapters. Separate reusable components from composed page views; do not duplicate
formatting, selection, review or task-state rules in renderers. The browser runs
the same presentation core in WASM. All real mutations use the same command
service and task engine. See [browser transport](api-and-interfaces.md#browser-html-and-live-updates).
Bundle pinned CSS/JavaScript/WASM assets locally; no CDN is required. The isolated
mock preview may be developed before backend integration, but cannot make real
operational claims. See [UI design](ui-design.md) for scope and interaction rules.

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
by hiding its disabled state. Test shared field errors/actions across server/TUI,
and identical template outputs/field validation across native/WASM preview runtimes.

### Browser implementation

Use go-app v11 for the browser shell and reusable DOM components, following the
progress reference. Feed typed API snapshots/events into shared Go controllers;
render view models rather than implementing a second application state machine.
Use the existing JSON SSE contract, not a parallel HTML event stream. Mount and
dismount own bounded timers/subscriptions; dispatch asynchronous results onto the
UI loop. Preserve focus, scroll and unsaved edits across live updates. Confirmations
are accessible modal dialogs with focus containment/restoration and keyboard parity.
Render logs as text. Motion preferences include the system reduced-motion setting.

### TUI implementation

Use Charm's [Bubble Tea](https://github.com/charmbracelet/bubbletea) for interaction,
[Bubbles](https://github.com/charmbracelet/bubbles) for reusable controls, and
[Lip Gloss](https://github.com/charmbracelet/lipgloss) for styling/layout. Use v2
of Bubble Tea, Bubbles and Lip Gloss (`charm.land/.../v2`), plus Harmonica for spring
motion (currently v0.2; no v2 release exists). Keep terminal controls in the adapter;
renderer-independent motion may live in shared components. Use alternate-screen,
full-window background painting, layered confirmation popups, mouse clicks/wheel
interaction, and keyboard-complete equivalents. Provide semantic customizable
themes, reduced motion, clear focus/action hints, keyboard-complete navigation,
responsive layouts for small/resized terminals, and scrollable diffs/logs. Live
progress must preserve focus and scroll position; status must remain understandable
without color. Use the same validation, reviews, and task data as the browser.
Host configuration forms include the [live file preview/editor](configuration-generation.md#live-preview-and-quick-edits)
inside the TUI. Use an embedded multiline control with native shared rendering;
form edits refresh previews and direct file edits refresh diffs without leaving the screen.

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
| `nx.<cluster>.secret.<node>` | Secret import to the selected holder | Core request/reply only; restricted grants, no persistence or payload logging |
| `nx.<cluster>.activity.<node>` | Latest host telemetry sample, stream ID/sequence | Core, no persistence/ack/replay; latest-value delivery |
| `nx.<cluster>.result.<node>` | Command/task ID, executor, versioned outcome | JetStream, deduped by task/command receipt |
| `nx.<cluster>.log.<node>.<task>` | Absolute offset, bytes, owner ID | Core; recover from persisted logs |
| `nx.<cluster>.progress.<node>.<task>` | Versioned progress snapshots and activity updates | Core; recover latest persisted snapshot via query |
| `nx_state` KV | Versioned membership, refs, host/task projections | Watch plus snapshot reconciliation |
| `nx_git` Object Store | Retained local Git increments, never remote base snapshots | File-backed JetStream; bounded storage with explicit retention/GC |

Enforce [machine permissions](identity-and-trust.md#managed-tls-and-nats-permissions)
through NATS accounts and accepting-broker grants, not subject prefixes alone.
Full-member services can submit work to any executor and read cluster-wide task
state, logs, and progress. A node publishes logs/progress only under its own prefix;
that write restriction does not restrict authorized peer subscriptions. Gateways
query the owner for snapshots/log gaps and stream live updates back to the requesting
console. Pending leaves retain scoped reads and remain non-voting.

Commands expire after 120 seconds if not admitted. Broker deduplication is an
optimization; durable request fingerprints/receipts enforce retry identity.
Use discard-new backpressure on full command queues. Error outcomes must distinguish
rejected before admission from unknown after submission.

Proposed initial bounds: command payload 256 KiB; query chunk 64 KiB; total bounded
query 64 MiB; log event 64 KiB. Larger Git artifacts use chunked object storage,
not oversized command payloads. UI event cadence: coalesce state changes up to
250 ms; host telemetry follows the [cadence below](#host-metric-delivery-and-lifecycle).
Inventory refreshes every 30 seconds, heartbeat every 10 seconds. Mark reachability
stale after 20 seconds without a heartbeat, offline after 35.
Thresholds are protocol/config constants with tests, not duplicated UI timers.

Subscribe before reading a snapshot/log tail, then merge by resource version or
byte offset. Reconnection starts a fresh snapshot; safety refresh catches missed
final updates. Telemetry never renews membership by itself.

Enrollment exchanges public CSRs/certificates over HTTP; approval enables authenticated
NATS access. Secret values use the [restricted Core path](identity-and-trust.md#secret-delivery-over-nats),
not JetStream. After a lost result, query the durable receipt before retrying;
never bypass admission or repeat uncertain effects through another transport.

## Nix progress adapter

Consume machine-readable Nix activity output at the executing node and normalize
it into task progress; do not scrape ANSI progress bars from NH/nom. Nix exposes
`--log-format internal-json`, which is the proposed input, with supported versions
and activity types tested explicitly rather than assumed to be a stable wire API.
[Nix logging formats](https://nix.dev/manual/nix/2.25/command-ref/new-cli/nix3-log)

Collect download/cache activity, byte counters, derivation builds, and remote
executor information from actual events; supplement with bounded plan/store queries
when needed. Capture each host's build and activation activities at its executor.
Unsupported fields/events produce an explicit unavailable/degraded telemetry state
and retain sanitized logs; they must not crash or alter the underlying task outcome.
Do not infer completed work from text fragments or terminal cursor movement.

Use the shared presentation core for percentage/rate/unknown-state formatting.
Coalesce progress up to 250 ms while delivering terminal/failure transitions
promptly. Bound active previews and paginate detailed lists. Test adapter fixtures
for supported Nix versions plus actual cache substitution and local fallback in E2E.

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

### Host metrics

One in-process OS sampler per node feeds the [live telemetry contract](host-inventory-and-lifecycle.md#live-host-telemetry)
and optional OTel SDK/OTLP export; no Collector is required. Bound sampling work and
isolate slow filesystem/device reads. Local sampling and views continue through
quorum loss, NATS disconnection, or OTLP failure; neither delivery path blocks the other.

Use [OTel system conventions](https://opentelemetry.io/docs/specs/semconv/system/system-metrics/)
for CPU, memory/paging, filesystem, disk/network counters, and uptime;
`nixmox.host.*` only where no standard fits. Pin the convention version and mapping
without coupling the UI schema to SDK upgrades. Export cumulative I/O counters,
omit stale/unavailable measurements, and identify daemon producers by resource
`nixmox.node.id`; `host.name` is the actual OS hostname, not a node alias.
Do not use a Nixmox node ID as `host.id`. Command/task IDs belong in logs/traces,
not metric dimensions.
Each node exports only its own metrics; configure one producer per metric per
pipeline, including when using a separate hostmetrics receiver.

### Host metric delivery and lifecycle

Intervals and defaults live in [runtime settings](runtime-configuration.md#option-reference).

| Work | Timing | Behavior |
| --- | --- | --- |
| CPU, memory/swap, disk/network counters, load, uptime | `telemetry.host.fastIntervalMs` | Update shared measurement cache; publish NATS snapshot immediately |
| Filesystem space | `telemetry.host.filesystemIntervalMs` | Independent worker; preserve its own timestamp/freshness |
| Browser/TUI | On sample; coalesce at most 250 ms | Replace pending updates independently of inventory refresh |
| OTLP | `telemetry.otlp.metricsIntervalMs` when enabled | SDK reader consumes the same cache, without resampling the OS |

NATS and OTLP share measurements, identity, and counter semantics, but use separate
formats and schedules. OTLP aggregation/export may omit intermediate gauge samples.

| Path | Buffering and recovery |
| --- | --- |
| Core NATS activity subject | Exclude from JetStream; separate telemetry connection, reconnect buffering disabled; subscriber limits 128 messages / 4 MiB |
| Application/gateway | One pending latest snapshot per host; count drops/coalescing; expire gateway samples after five minutes without refresh |
| Reconnect/slow consumer | Publish/read the current snapshot; no backlog replay or durable telemetry consumers |
| OTLP | Serialize exports; two-second timeout including retries; skip ticks while exporting, then collect current values; no unbounded retry queue |

| Lifecycle | Action |
| --- | --- |
| Agent start | Start local sampling, including unjoined nodes (`membership_epoch: null`); publish cluster samples only while authorized |
| Restart/rejoin | New `stream_id`, sequence and rate baselines; retire old stream on authenticated restart |
| Receive | Reject obsolete membership epochs and older sequences within a stream |
| Counter reset/device hotplug | Invalidate only affected rate baselines |
| Leave/removal | Close subscriptions, discard pending cluster samples, evict that membership's gateway cache |
| Shutdown | Stop workers; bound final OTLP flush to two seconds |

### Application diagnostics

Structured logs include service name, node/cluster/command/task IDs, event, and phase.

Resource identity applies consistently to OTLP traces, logs and metrics:

| Attribute | Value |
| --- | --- |
| `service.name` | `nixmox-daemon-<friendly-name>` (node ID fallback), `nixmox-cli`, `nixmox-tui`, or `nixmox-browser` |
| `service.instance.id` | Stable daemon node ID; never a potentially colliding friendly name |
| `host.name` | Actual OS hostname; omitted in browsers where unavailable |
| `nixmox.node.id` | Local daemon node ID; omitted by clients |
| `nixmox.node.name` | Optional friendly daemon name |
| `nixmox.cluster.id` | Local daemon's committed cluster membership; omitted when unjoined |

Clients describe themselves, not the target daemon. Target IDs belong on operation
spans. SDK resources are immutable: membership changes select a fresh resource
for newly started spans and newly emitted logs, including cached library
instrumentation. Existing spans and queued logs keep their original resource.
Metric exports use current membership; cumulative counters retain process-lifetime
semantics rather than resetting on join/leave. Batch processors/exporters remain
process-owned, bounded, and shut down once after workers stop.

Propagate standard trace context through command/query envelopes and child tasks.
Example span chain: `command.admit → task.execute → git.materialize → nix.build →
deployment.activate → deployment.observe`. Normal asynchronous execution keeps
its originating trace; recovery starts a new attempt linked to the persisted
origin. Traces do not determine task success.

Enrollment has an `enrollment.join` span across submission, approval waiting,
credential installation and `enrollment.connect` through authentication.
The authority persists normalized W3C origin context on the join request, outside
the signed proof. Independently initiated approval/rejection commands keep their
own traces, with an `enrollment.decision` link to that stored origin. Trace data
never authorizes enrollment or affects request identity/idempotency.

Background operations have finite root spans (`membership.poll`,
`topology.reconcile`, `tasks.reconcile`, `presence.publish`), with HTTP/NATS calls
as children—not one indefinitely open worker span. Routine background roots use
10% head sampling with parent-based propagation; interactive/enrollment roots
remain fully sampled. Unsampled background failures cannot be retroactively
sampled. Daemon friendly names affect service grouping; unique instance/node IDs
remain necessary when names collide (including local multi-daemon checks).

Metrics: command admission latency, queue depth, phase duration, failures by error
code, broker reconnects, Git bytes transferred, artifact storage failures, log catch-up,
cache lookup latency, streamed bytes, peer failures, and stale hosts. Command/task
IDs belong in logs/traces, not unbounded metric labels.
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
distributed behavior. Check server/TUI presentation parity and native/WASM preview parity.
Physical boot/media claims require disposable-device evidence; VMs alone are insufficient.

| Area | Required checks |
| --- | --- |
| Configuration | Generate without host/write credentials; template selection/pinning; safe serialization/paths; incomplete commits; hardware with/without filesystems; multi-disk layouts; browser/TUI live preview and embedded edits; preserve manual edits/buffers through regeneration, resize, and version conflicts; validate exact edited files |
| Interface parity | Same operations, defaults, validation, reviews, errors, and results; explicit unknown/stale state; every verb maps to a registered operation/read |
| Browser | go-app component/action parity; modal focus/keyboard behavior; SSE reconnect/version ordering/log gaps; local assets; live updates preserve focus, scroll and unsaved edits |
| Host telemetry | Fast/slow cadence and timestamps; NATS/OTLP same-source values with independent delivery; slow consumers/exporter failures; restart/rejoin/hotplug; no stale replay; cache expiry; browser/TUI/CLI parity; null/stale values; samples never invalidate reviews |
| Reviews | Start without advance erase approval; reject stale bindings/missing acknowledgements; unrelated health/progress does not invalidate approval; duplicate resume cannot repeat effects |
| Reads | Pagination/filter bounds, revision-pinned catalogs/files, log gaps/per-build logs, download expiry and unavailable owners |
| Admission/recovery | Interrupt every admission boundary; retry resolves the same task; restart queued/running/cancelling work; uncertain effects reconcile without destructive replay |
| Local scope/quorum | Network/discovery/join intent survive restart without quorum; cluster mutations block; stale reads stay labeled; no fallback or lease-only takeover |
| Cluster stress | Three-node concurrent command/artifact load with repeated broker restarts, leader loss, and network partition/heal; majority continues, minority rejects durable mutations; recovery preserves acknowledged data and receipt identity without duplicate effects; bounded queues/memory; record workload, duration, and results |
| Enrollment | HTTP CSR/approval/issuance retries; pinned cluster trust; promotion/removal/rejoin; signed removal over NATS or HTTP; reject invalid signatures and old epochs |
| Trust/secrets | Broker/leaf grants, rotation/revocation, issuer preservation; scoped Core secret delivery and lost replies; no raw secrets in JetStream, Git, logs, or diagnostics |
| Transport | Plain HTTP console/enrollment/cache; NATS-only TLS; secret delivery through NATS; custom ports, firewall, reservations, occupied listeners |
| Installer bootstrap | Linux SSH bootstrap without Nix; privileges/host keys/cleanup; automatic TUI, headless Ethernet, Wi-Fi hotplug/errors, discovery timeout/retry/late results, joining from either side |
| Installation | USB/SD, kexec, and reboot converge; incompatible entry fails before effects; media/boot/erase approvals separate; same identity/output returns; single-node/issuer state survives; no repeated partitioning |
| Preparation/SOPS | Reuse unassigned configuration; reject swapped/duplicate disks; confirm every erase target; no-SOPS install; updater failures; exact rule selection, all key groups, shared aliases, idempotent recipient edits |
| Media/recovery | Physical profiles, compressed image capacity/readback, selected builders/emulation, cache failure/local fallback; recovery survives Apply/rollback/GC |
| Apply | Local/default/remote-target rules; unpublished remote build; pinned source through regeneration; manual/auto approval and restart; changed baseline; boot staging; activation interruption and target exclusion |
| Rollback/health | Retained/missing outputs; desired/running drift; exact output verification and separately reported health; no automatic rollback |
| Diff/progress | Added/removed/version/output/ambiguous changes; target baseline; changing/unknown totals, cache sources/rates, retries, simultaneous builds/downloads, reconnect; byte completion never implies deployment success |
| Cache | Signed HTTP substitution; streaming/cancellation/GC; unauthenticated readers; key loss/recovery; builder offline with target fallback; same pinned source; unsupported-system misses |
| Git | Independent repositories; writer loss; missing base/artifact; concurrent edits and upload/CAS restart; conflict abort; pinned old commits; PR push versus upstream inclusion; reviewed deletion; no full base in JetStream |
