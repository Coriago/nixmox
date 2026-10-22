# Rewrite implementation ledger

## Sequence and current boundary

All thirteen specification documents were read before scaffolding.

1. Specification review: complete.
2. Initial implementation scaffold: complete; no tests or feature effects.
3. Engines: in progress. Research and reusable implementations are recorded in
   `engine-research.md`; implementation and validation status is below. This is
   not yet a complete or production-ready engine layer.
4. Feature services: not started.
5. Stateful Nix VM acceptance suite: not started.
6. Disposable-host validation: not started.

A scaffold interface is not an implemented capability. Unwired operations return
UNSUPPORTED_CAPABILITY; no simulated receipts, successful effects, anonymous broker,
or listening daemon are supplied. Do not enable the service in production yet.

## Dependency direction

- `internal/domain`: wire/persisted models, typed mutation payloads, errors and
  protocol constants; standard library only.
- `internal/engine`: shared contracts only. Implementations and focused tests are
  grouped in `engine/comms`, `engine/telemetry`, `engine/task`, `engine/nix`, and
  `engine/git`. Git/Nix use the task runner; task execution never imports Git/Nix.
  Small pure utilities live in `engine/internal/shared`; test-only runner helpers
  live in `engine/internal/testutil`. No feature-specific copies of engines.
- `internal/service`: one operation registry/admission boundary, feature handler
  attachment, queries and secret ingress. Local and cluster scopes share receipts
  and tasks; scope never changes on transport failure.
- `internal/presentation`: native/WASM-safe forms, previews, action availability,
  progress and editing contracts. No OS, transport or terminal dependencies.
- `internal/console`: HTTP client/gateway, CLI and terminal adapter entry points.
  These consume the same service and presentation contracts.
- `internal/app`: composition and lifecycle; the only layer wiring all engines.
- `internal/runtime`: runtime configuration and the canonical defaults JSON,
  also read by `nix/module.nix`.
- `cmd/nixmox` and `cmd/preview`: thin native and browser-WASM entry points.
- `nix`: package/service/installer definitions. Board recipes remain gated.

Models are split by subject for navigation, not by transport. Typed per-operation
payloads prevent a permissive map from becoming an arbitrary execution interface.
JSON extension points are limited to template values, review-specific bindings,
diagnostic details, and operation-specific results.

## Engine implementation order and gates

1. NATS/JetStream: embedded listeners and independent mTLS policies; explicit
   broker key allowlists and accepting-leaf grants; one-to-three replica topology,
   bounded discard-new command streams, KV CAS, incremental object storage,
   explicit ack/recovery and scoped Core request/reply. Prove pending nodes cannot
   access JS/system APIs. Never persist telemetry or secrets.
2. OTel: independent bounded exporters and one shared OS sampler; trace context
   across HTTP/NATS/children, correlated sanitized logs, bounded-cardinality
   metrics, freshness and stream/epoch handling. Diagnostic collector is optional
   development infrastructure, not a runtime prerequisite.
3. Tasks: fsync/rename execution records, stable request hashes and deterministic
   task IDs, exclusion/fencing, intent/observation checkpoints, reconciliation,
   immutable terminal states, append-only offset logs and process-group shutdown.
   No takeover or destructive replay based on leases.
4. UI core: research/design in `ui-core-design.md`; reusable resource items/lists,
   fields/reviews/editor state, telemetry graph projections and bounded history,
   semantic themes, async/submission/log mechanics and shared CLI/TUI controls.
   Use small component probes, not actual screens or workflows. Browser WASM stays
   preview-only; actual HTML/htmx/SSE and CLI/TUI composition belong to stage 4.
5. Nix: pinned argv plans, no IFD during lightweight validation, internal-json
   activity normalization, structured closure diffs, output roots and signed NAR
   streaming using go-nix. Apply cannot reach Disko/install methods.
6. Git: isolated worktrees, independently fetched remote base, prerequisite-checked
   incremental bundles, upload-before-ref-CAS, pinned materialization and publication
   reconciliation. No full base bundles, implicit push, or force-push.

Identity/enrollment and cache ports are explicit because they constrain NATS,
installation and Nix engines. CSR approval/issuance, pinning and key possession
must be proven before enrollment is advertised.

## Feature coverage after engines

- Identity, discovery/network, cluster enrollment, health/inventory/telemetry,
  membership transitions and signed removal.
- Repository import/fetch/rebase/publication/GC; credentials and secret staging.
- Configuration catalogs/templates, editable drafts, validation and commit.
- Hardware/disk observations, preparation, SOPS AST updates and source acceptance.
- Candidate builds, structured diffs/reviews, Apply/rollback and generation pins.
- Installer/media recipes, release verification, device plans/readback, boot
  adapters, preserved handoff and install orchestration.
- Cache gateways, diagnostics, all documented reads/downloads and interface parity.

Stage 5 follows the acceptance matrix in technical-requirements.md with a shared,
long-lived VM happy path before failure/stress flows. Physical boot claims remain
disabled until stage 6. Before physical transitions verify host/device identities,
preserve Wi-Fi/SSH/trust and establish recovery; no developer-host device effects.

## Validation for the scaffold

Use the locked flake devshell for gofmt, go build, go vet, WASM compilation and Nix
module evaluation. No Go tests or VM tests are introduced in stage 2. Engine
dependency pins and engine-specific tests follow the corresponding research.

Validated during scaffolding:

- Native Go build and vet; static Linux ARM64 cross-build and editor WASM build.
- Locked Nix package build and flake output evaluation.
- Generated module configuration accepted by the strict runtime YAML parser.
- Custom HTTP port, interface-scoped firewall, merged existing port reservations,
  and preservation of mixed-case repository/credential/profile aliases.
- Scaffold daemon exits unavailable (code 3), without starting engines.

Development invocation while files are untracked: `nix develop path:.`.
Dependencies are now pinned in go.mod/go.sum; the flake vendor hash includes the
engine dependencies. Nix package checks run the focused Go suite, including Git
fixtures and a local embedded NATS broker, without VMs or real-host effects.

## Engine implementation status

Implemented primitives:
- Comms: embedded broker/mTLS key policies, subject grants, bounded Core queries,
  live messages, JetStream deliveries/acks, KV CAS/watch, verified bundle storage,
  and quorum observations.
- Telemetry: bounded owned OTel providers, trace propagation/link primitives,
  sanitization, cached host sampling, and cached-sample metric observations.
- Tasks: durable admission/journal, deterministic identities, phase exclusions,
  intent/reconciliation, process-group ownership, offset logs, and persisted
  progress/activity snapshots. Generic adapters attach to task/phase definitions;
  wrapped runners retain raw streams before parsing. Parser failures cannot
  change command success or task lifecycle.
- UI: shared typed native/WASM preview and editor state, action/progress views,
  and a Bubble Tea editor.
- Nix: pinned plans, IFD-disabled validation, internal-json adapter, structured
  closure comparison, exact-output activation plans, output roots and go-nix
  NAR serialization.
- Git: independent remote fetch, pinned private worktrees, reviewed-file commits,
  prerequisite-checked incremental bundles, import verification, isolated rebase,
  and explicit non-force publication with remote observation.

Focused tests cover single-broker authorization/delivery/storage, task admission
and uncertain-effect recovery, exclusions, logs and subprocess cancellation,
generic adapter framing/failure isolation/EOF/sensitive output, Nix plans and
metadata/progress/diffs, and Git binary-preserving incremental exchange and
publication using temporary local repositories.

Remaining reusable-engine work and later validation boundaries:
- Multi-broker promotion, routed pending-leaf restrictions, revocation/reconnection
  and degraded-quorum acceptance belong to later distributed validation, not the
  current UI-core work. Single-node tests do not establish these properties.
- Wire HTTP/NATS/task trace propagation end to end, exercise exporter failure
  isolation and sampler freshness, and add optional development diagnostics.
- Finish task worker/retry integration and live progress/activity publication;
  current scheduler exposes explicit phase execution, not an operational daemon.
- Build the reusable UI component/interaction layer described in `ui-core-design.md`.
  Telemetry graph models, generic resource items, semantic themes and common
  interactive CLI/TUI controls are planned, not implemented yet. Defer actual
  HTTP/htmx/SSE routes, browser pages and CLI/TUI navigation to feature composition.
- Complete cache signing/policy and Nix store integration validation. Git
  credential holders and provider PR/merge adapters require explicit wiring.
- Add the remaining focused engine tests before proceeding to feature workflows.

No VM E2E or disposable-host tests have been run. Production activation remains
gated; the application composition root continues to return unavailable.
