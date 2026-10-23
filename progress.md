# Progress

## Backend engines

Reusable primitives exist, with focused Go tests; the operational application is
not wired yet.

- `internal/engine/comms/`: embedded NATS, TLS/key authorization, Core messaging,
  JetStream delivery, KV CAS/watch, incremental bundle storage and quorum observations.
- `internal/engine/telemetry/`: bounded OpenTelemetry providers, trace propagation,
  sanitized logs, shared host sampling and metrics.
- `internal/engine/task/`: durable journal, admission/state transitions, phase
  execution, uncertain-effect reconciliation, subprocess cancellation, offset logs
  and reusable progress/log adapters.
- `internal/engine/nix/`: pinned command plans, evaluation/build/activation helpers,
  structured closure diffs, NAR serialization and Nix progress parsing.
- `internal/engine/git/`: isolated worktrees, pinned commits, incremental bundles,
  verified imports, rebase conflicts and non-force publication.

## Directory map

- `internal/engine/*.go`: contracts; subdirectories contain implementations/tests.
  Shared engine helpers live in `internal/engine/internal/`.
- `internal/domain/`: typed data models and operation payloads.
- `internal/service/`: operation registry and service boundaries; handlers remain unwired.
- `internal/app/`: composition root, still gated; `internal/runtime/`: config/defaults.
- `cmd/nixmox/`: unavailable application stub. `nix/` and `flake.nix`: packaging,
  module/installer scaffolds and development tooling.
- `docs/`: specifications, engine research and remaining-work inventory.

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

## Next chunk: basic cluster management

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
- `internal/cli/` **(new)**: parsing, typed commands, help, output and exit codes.
  Implement the in-scope cluster/discovery, enrollment approval/rejection, host
  list/show/connectivity/telemetry, shutdown, leave/remove and task status/log/watch
  commands from the specs. Support endpoint selection, selectors, expected versions,
  confirmations, JSON output and wait/detach behavior. No new TUI or prompt framework.
- `internal/client/` **(new)**: reusable typed HTTP API client, error decoding,
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
- `internal/gateway/` **(new)**: implement and wire the working HTTP API **in this
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
- `internal/engine/identity/` and `internal/engine/platform/` **(new implementations)**:
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
