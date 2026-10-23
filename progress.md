# Progress

## Backend engines

The daemon, HTTP API and CLI now run standalone nodes and provisional multi-node
clusters. Full membership, HA promotion and the remaining lifecycle are in progress.

- `internal/engine/comms/`: embedded NATS, TLS/key authorization, Core messaging,
  JetStream delivery, KV CAS/watch, incremental bundle storage and quorum observations.
- `internal/engine/telemetry/`: bounded OpenTelemetry providers, trace propagation,
  sanitized logs, shared host sampling and metrics.
- `internal/engine/task/`: durable journal, admission/state transitions, phase
  execution, uncertain-effect reconciliation, subprocess cancellation, offset logs
  and reusable progress/log adapters.
- `internal/engine/nix/`: pinned command plans, evaluation/build/activation helpers,
  structured closure diffs, NAR serialization and Nix progress parsing.
- `internal/engine/git/`: go-git v5.19.3 for supported operations; isolated Git CLI
  adapter for bundles, rebase and linked-worktree administration.
- `internal/engine/identity/`: persisted machine keys, atomic cluster genesis/CA,
  CSR validation, idempotent issuance, pinned trust and joined restart recovery.
- `internal/engine/platform/`: management-interface network observations only.

## Directory map

- `internal/engine/*.go`: contracts; subdirectories contain implementations/tests.
  Shared engine helpers live in `internal/engine/internal/`.
- `internal/domain/`: typed data models and operation payloads.
- `internal/service/`: registry, cluster/enrollment orchestration, durable admission
  and host/task/telemetry projections.
- `internal/app/`: daemon lifecycle; `internal/runtime/`: Viper config,
  canonical defaults, strict validation, flag precedence and listener selection.
- `internal/cli/`: Cobra commands; `internal/client/`: typed HTTP client and
  uncertain-submission receipt lookup; `internal/gateway/`: Gin API.
- `cmd/nixmox/`: thin executable. `nix/` and `flake.nix`: packaging,
  module/installer scaffolds and development tooling.
- `docs/`: rewrite specifications.

## UI reference — frozen

`internal/ui/progress/` is the reference pattern: shared Go control logic and view
models, with thin go-app/browser and Bubble Tea/terminal adapters. `demo.go` supplies
mock data; `cmd/progress-preview/` runs the standalone previews. Follow this pattern,
but do not assume this exact component must be used immediately.

**Freeze UI development until real backend data can be produced.** This supersedes
“finish the entire UI core first.” Finish backend foundations and operational
single-node task execution/recovery, then validate the UI pattern against real
events and logs. The next chunk is the cluster-management scope below, not GitOps;
no more mock galleries or UI polishing.

## Current chunk: basic cluster management

### Completed foundation

- Viper uses canonical defaults, one YAML document and explicitly changed Cobra
  flags; unknown fields/types fail. Catalog aliases retain case and punctuation.
- Cobra powers both executables. The operational CLI uses HTTP only; `serve`
  launches the daemon. Read commands cover identity, cluster status, enrolled hosts,
  endpoints/telemetry, network status, tasks, logs and command receipts.
- The unjoined daemon owns the journal lock, persists identity/private keys, starts
  a loopback-only mTLS broker, recovers/drains local tasks, samples real telemetry
  and serves Gin on explicit management addresses. Shutdown unwinds workers and
  resources. No full membership or cluster quorum is fabricated.
- `network check [--wait|--detach] [--command-id ID]` follows CLI → Gin → service →
  task engine → platform observation. Task intent, evidence and logs are durable.
- Gin exposes command/receipt reads, host/task/network reads and scoped live
  telemetry SSE. Request bounds, strict command JSON, error envelopes, untrusted
  proxy headers and cross-origin mutation protection are enforced.
- The client reconciles lost submission responses against the original receipt;
  it never silently resubmits with a new ID.
- Go integration tests exercise actual loopback HTTP/NATS, CLI execution, restart,
  stable identity, queued-task recovery, duplicate receipt/task handling,
  lost-response reconciliation, telemetry/SSE, private file permissions and
  rejection of plaintext NATS. No real power effects are invoked. The full Go suite,
  targeted race tests, vet and both Nix package builds pass.
- go-app is v11.1.3; native/WASM and browser-reference interactions were verified.
  Browser/TUI feature work remains frozen.

### Implemented: provisional multi-node cluster

- `cluster create NAME` persists genesis and issuer material before exposing a
  single durable control plane. It remains pending and reports “No full members”.
- `cluster join --address HOST:PORT --name NAME` submits locally signed daemon
  and broker CSRs. `cluster requests list/show/approve/reject` reviews exact
  request versions; membership decisions/command claims use JetStream KV CAS.
- Approvals issue stable, public certificates; private keys stay on their nodes.
  Enrollment pins cluster identity and broker keys. Unapproved nodes have no
  cluster NATS access; approved nodes use scoped mTLS leaves, not durable replicas.
- Joined startup restores trust and committed grants before accepting remote
  traffic. Leaf authorization changes restart the embedded broker (NATS cannot
  reload all these fields); connection-generation cancellation reopens consumers.
- Real heartbeats and telemetry flow over Core NATS. The authority checks subjects,
  membership epochs and boot streams, retains latest observations and marks stale
  hosts without changing membership. Local/remote reads share the sampler schema.
- Manual CLI runs verified creation, two approvals, three live hosts, matching
  metric fields/capacities, restart/reconnect and repeated command identity.
  Existing Go checks still pass; no new test suite was added for this slice.
- Temporary smoke command: `nix develop -c bash scripts/smoke-cluster.sh`.
  It starts three isolated data directories/configs/port sets on the same OS,
  drives only CLI operations, compares real telemetry and cleans up its processes.
  No VMs, fake metrics, power operations or deployment are involved. Dynamic
  utilization/counters/timestamps are expected to differ slightly between samples.

### Next implementation slice / remaining work

- Implement discovery/configured autojoin, remote task routing and owner reads,
  then power reconciliation, leave/removal/revocation and rejoin.
- Add authenticated renewal/rotation and full-member promotion/three-replica
  topology once deployment/adoption evidence exists. Pending leaves are not voters.
- Discovery/autojoin and unimplemented lifecycle commands still fail closed.
  Enrollment currently retains at most 64 requests; retention/GC is still needed.
- The pending node exposes its own inventory; the provisional authority collects
  all enrolled hosts. Cluster-wide views on other gateways are not yet wired.
- Finish durable rejection/expiry handling and result projections for distributed
  delivery; commands currently target the addressed executor only.

General event snapshots, reconnecting client event/log watches, task controls and
read filters, expected-version checks beyond join decisions, interactive
confirmation/pending-ID storage, and remaining in-scope Cobra commands are not
implemented yet. Task reads support pagination, not filtering. Only host-telemetry
SSE is wired; unsupported operations are rejected rather than acknowledged as queued.

The complete chunk scope and acceptance criteria remain below.

Implement the cluster against the identity/trust, host lifecycle, runtime, task,
and API specifications. Assume agents are already running on the hosts.

**Scope:** daemon startup/recovery; cluster creation and discovery; host enrollment,
approval/rejection and scoped credentials; host inventory, connectivity, heartbeat
and telemetry; simple host-targeted tasks such as shutdown; task receipts/status/logs;
graceful leave, forced removal, revocation and reconnect reconciliation.

**Excluded:** GitOps, Nix deployment/adoption, media, SSH bootstrap, boot transitions
and installation. Browser/TUI development stays frozen until real backend data exists; the API-backed
CLI is included in this chunk.

### Code placement and engine reuse

**CLI → HTTP API → services → engines.** The CLI always calls the daemon's API,
even for localhost. It must not invoke services or engines directly. `serve` only
launches the daemon; it is not an alternative command-execution path.

- `cmd/nixmox/`: thin executable entry point for daemon and CLI.
- `internal/cli/`: parsing, typed commands, help, output and exit codes.
  Implement the in-scope cluster/discovery, enrollment approval/rejection, host
  list/show/connectivity/telemetry, shutdown, leave/remove and task status/log/watch
  commands from the specs. Support endpoint selection, selectors, expected versions,
  confirmations, JSON output and wait/detach behavior. No new TUI or prompt framework.
- `internal/client/`: reusable typed HTTP API client, error decoding,
  receipt lookup and reconnecting event/log watches. CLI calls this client, not
  engines or in-process service shortcuts; uncertain submissions query their original
  receipt identity rather than creating a new command.
- `internal/runtime/`: runtime configuration models, loading, precedence and
  validation—not daemon lifecycle or task execution. Own the canonical defaults
  (`defaults.json`), ports/listen settings, state paths and endpoint resolution.
  The daemon, CLI and NixOS module reuse these definitions rather than hardcoding
  separate defaults.
- `internal/app/`: daemon composition/lifecycle. Load configuration and identity,
  initialize persistence, start embedded NATS, recover tasks, start telemetry and
  HTTP, then shut down in reverse order. Support unjoined startup.
- `internal/gateway/`: implement and wire the working HTTP API **in this
  chunk**: command submission, receipts, enrollment, cluster/host/task reads and
  event/log streaming. Start it with the daemon and connect every in-scope CLI
  command to it. API handlers call the real services that execute operations through
  the engines—not mocks or unavailable stubs. Only browser pages/assets are deferred;
  they will later use this same HTTP server.
- `internal/service/`: cluster, enrollment, host and task orchestration in focused
  files. Reuse the existing registry, typed payloads and admission boundary; implement
  durable receipts, expected-version checks, routing and read projections here.
- `internal/engine/comms/`: keep NATS startup/TLS/listeners, messaging, KV/watch and
  JetStream mechanics here; `app` owns their lifecycle. Services decide membership
  policy. Use durable command/results delivery and the specified pending-leaf path,
  not a parallel HTTP execution endpoint.
- `internal/engine/task/`: reuse the journal, phase execution, resource exclusions,
  process runner, cancellation and log retention; finish worker/recovery integration.
  Persist admission and intent before effects; reconcile uncertain shutdowns rather
  than replaying them. Scheduled shutdown is not proof the machine powered off.
- `internal/engine/identity/` and `internal/engine/platform/`:
  local keys/CA, CSR-bound issuance, trust/removal verification; discovery and narrow
  host inspection/power adapters. Keep contracts in `internal/engine/*.go`.
- `internal/engine/telemetry/`: reuse the shared sampler and OTel providers; publish
  latest host samples over Core NATS, with freshness/stream sequencing. No durable
  telemetry history. Keep wire models in `internal/domain/` and defaults in
  `internal/runtime/`; do not duplicate them in adapters.

### Boundaries and acceptance

Approval yields **pending**, not full membership. Keep the creator's provisional
standalone broker and joining hosts' non-voting, restricted leaves; promotion needs
later verified deployment/adoption. Do not invent full membership for this chunk.
Removal requires committed, epoch-bound authorization and signed notice; disconnect
is not removal. Preserve quorum and sole-issuer restrictions.

Prove create → discover → request/approve → observe telemetry → dispatch/reconcile
a simple host task → leave/remove/rejoin through the CLI/API, including restart,
duplicate delivery, uncertain submissions, JSON/exit-code behavior and unauthorized
access. Use focused Go/integration tests with fake power effects;
comprehensive VM/physical testing remains later. Do not shut down real hosts merely
to demonstrate the API.
