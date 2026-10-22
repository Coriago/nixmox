# Core engine research and decisions

Step 3 implements reusable primitives, not application workflows. The daemon stays
gated until feature admission, enrollment and recovery are wired in step 4.

Implementation packages are `internal/engine/{comms,telemetry,task,nix,git}`;
the parent package contains contracts only. UI core is `internal/presentation`,
with native/HTTP adapters in `internal/console`.

Current pins: Go 1.26, nats-server 2.15.1, nats.go 1.54.0, OTel core/SDK 1.47.0,
OTLP log HTTP exporter 0.23.0, gopsutil 4.26.9, Bubble Tea 1.3.10,
Bubbles 0.21.1, Lip Gloss 1.1.0, and go-nix commit 4bdde671e0a1.
These are dependency pins, not claims of completed integration validation.

## NATS and JetStream

Context7: /nats-io/nats.go (durable consumers, explicit/double acknowledgements,
backoff, KV watches/CAS, discard-new). Primary sources:
- https://docs.nats.io/learn/security/encryption
- https://docs.nats.io/learn/security/authorization
- https://docs.nats.io/running-a-nats-service/configuration/leafnodes
- https://docs.nats.io/running-a-nats-service/nats_admin/jetstream_admin/disaster_recovery
- Pinned nats-server Options/authentication and nats.go jetstream sources.

TLS blocks are independent. Verify CA chains AND admitted public keys; certificate
possession is not human authentication. Client permissions and accepting leaf
permissions must prohibit peer writes and pending access to JS/system APIs.
Never grant unrestricted reply inbox access to leaves. Full services own durable
consumers; forwarding deliveries to leaves is application orchestration.

Commands use work-queue retention, bounded discard-new storage, explicit acks and
durable per-executor consumers. A publish timeout is unknown, not rejected.
Ack only durable receipts, not task completion; retries deduplicate locally.
KV revision is a storage CAS token, not the domain lifecycle version. A nil watch
entry marks snapshot completion. Object-store limits must reject writes, not evict
unpublished bundles. Verify content before import; storage ack is not materialization.
Only one or three replicas; never rescue or lower quorum because a node disappears.
Health observations cannot replace successful consensus writes.

## OpenTelemetry

Context7: /open-telemetry/opentelemetry-go. Sources:
- https://opentelemetry.io/docs/languages/go/instrumentation/
- https://opentelemetry.io/docs/specs/semconv/system/system-metrics/
- https://pkg.go.dev/go.opentelemetry.io/otel/sdk/metric
- https://pkg.go.dev/go.opentelemetry.io/otel/sdk/log

Use owned providers, not process-global SDK mutation. W3C TraceContext travels in
headers/envelopes; asynchronous execution links to its origin. SDK bounded batch
processors never block tasks. Set exporter AND reader timeouts to two seconds and
disable long retry schedules. Metrics export samples current cache, never the OS
again. IDs belong in logs/spans, never arbitrary metric labels. Sensitive process
output is excluded, not assumed safe because logs are structured.

## Tasks and processes

Sources: https://pkg.go.dev/os/exec and https://pkg.go.dev/os.
CommandContext kills only the direct child by default. Use argv, an explicit
environment, process-group cancellation and bounded pipe waits. Wait must reap the
child once. External effects are not transactions: persist intent first and
observed completion afterward. A restarted uncertain phase only reconciles; it
must never run automatically. Terminal states are immutable. File durability needs
file fsync, rename and directory fsync; an atomic rename alone is insufficient.

Log adapters are generic task middleware, not Nix-specific scheduler logic.
`TaskDefinition.Adapters` and `Phase.Adapters` contain factories that create fresh
state for each matching subprocess. Handlers inject `Execution.Runner(base)`
into Git/Nix/other command adapters. The wrapper retains raw stdout/stderr with
absolute offsets, then feeds bounded records to parsers. Sensitive output bypasses
retention, adapters and caller collectors. `task.LineAdapter` supplies separate
bounded stream framing, oversized-record recovery, and EOF flushing.

Adapters emit progress, activities and activity logs, never lifecycle transitions
or effect authorization. Parser errors/panics degrade telemetry without changing
process success; raw-log storage failures remain real execution errors. Activity
IDs are namespaced by subprocess/adapter, and derived log completion means stream
EOF, not successful activity completion. One aggregate-progress producer is
allowed per subprocess; combining producers requires an explicit reducer.
`Journal.Progress` and `Journal.Activity` expose persisted derived observations.

`nix.LogAdapter()` is the first adapter. Package differences are not a log
adapter: they compare exact structured closure metadata. No feature handler
needs to know how Nix output is framed or parsed.

## UI core

Context7: /charmbracelet/bubbles; pinned compatible Bubble Tea 1 / Bubbles 0.21 /
Lip Gloss 1. Sources: https://htmx.org/docs/ and
https://htmx.org/attributes/hx-preserve/ plus pinned textarea/viewport source.
Async work returns messages, not concurrent model mutations. Resize updates sizes
without replacing input controls. Keep focus/scroll and manual buffers independent
of progress. hx-preserve alone does not guarantee textarea focus/cursor; place the
editor outside refresh targets. HTML uses Go escaping, Vary: HX-Request, local
assets and one JSON SSE stream. Preview uses the same pure Go code in WASM/native;
no Nix evaluation, network fetch, or authority in a preview.

## Nix

Sources:
- https://nix.dev/manual/nix/2.25/command-ref/new-cli/nix3-build
- https://nix.dev/manual/nix/2.25/command-ref/new-cli/nix3-path-info
- https://raw.githubusercontent.com/NixOS/nix/2.25.5/src/libutil/logging.hh
- https://pkg.go.dev/github.com/nix-community/go-nix/pkg/nar

internal-json is version-sensitive: normalize known start/stop/result IDs and
degrade unknown records; never scrape nom/NH ANSI output. Activity termination is
not proof of task success. Build pinned worktrees with locked inputs and no lock
updates; lightweight eval disables IFD. Apply's activation plan accepts only a
verified store output and switch/boot, never a flake or Disko. Diff exact closures,
retain ambiguous package matches and unknown metadata. NAR streaming uses go-nix,
holds output roots and does not introduce an archive cache.

## Git

Sources: https://git-scm.com/docs/git-bundle,
https://git-scm.com/docs/git-worktree, https://git-scm.com/docs/git-update-ref.
Use git plumbing through the shared argv runner; suppress interactive prompts,
ambient credentials, hooks and inherited Git configuration. Fetch independently
from the remote; detached private worktrees materialize pinned commits.
Bundles must exclude verified remote-base prerequisites; verify before importing.
update-ref with expected old OID provides local CAS, but JetStream's repository
refs record remains authoritative. Push uses exact commit/branch, never force;
uncertain provider results require observation, not an automatic repeat.
