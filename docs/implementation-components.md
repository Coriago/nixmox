# Implementation components

Proposed implementation boundaries for [requirements.md](requirements.md). External commands and routes are in [cli-api-reference.md](cli-api-reference.md). Package paths below are suggestions, not an inventory of implemented code.

## Libraries

| Library | Use |
| --- | --- |
| go-git | Repository access, revision resolution, worktrees, commits, and pushes. |
| Gin | HTTP routing and middleware. |
| Cobra | CLI commands, flags, and help. |
| Viper | Configuration files, environment variables, and flag bindings; not shared application state. |
| OpenTelemetry | Application-wide traces, metrics, and correlated logs. |

## Instrumentation and context propagation

- Instrument all application layers, not only host collectors. Use meaningful spans for commands, HTTP requests, NATS publish/consume operations, and domain/task phases rather than every helper function.
- A CLI command can start a trace that continues through the API, NATS, and the executing host: `CLI → HTTP client → Gin handler → NATS producer → consumer → task phases`.
- Pass `context.Context` through component boundaries. Use OpenTelemetry HTTP client/Gin instrumentation and W3C trace-context injection/extraction in HTTP and NATS headers, including request/reply and JetStream messages.
- For work triggered from persisted records rather than message headers, retain the originating trace context with the work dispatch metadata. Restore it when processing queued work; use span links for retries or resumed executions where a new trace is appropriate.
- Give asynchronous work its own execution lifetime; preserve tracing relationships without tying a durable task to the HTTP request's cancellation or holding request spans open until task completion.
- Use stable operation names and span kinds, host/task correlation attributes, error recording, and span status. Never attach secrets or raw credential payloads to spans, baggage, or logs.
- Centralize provider, propagator, sampling, and exporter setup; flush telemetry on daemon and CLI shutdown. Verify end-to-end trace continuity across HTTP and NATS in integration tests.

## State ownership

- NATS is the source of truth for shared records. Fetch them when needed; do not maintain duplicate in-memory registries or watch-backed mirrors by default.
- Do not keep a local members list; query membership through NATS.
- Keep required local state: node identity, startup/resume information, active runner handles, connections, and working data for an operation.
- Retain shared data locally only for a specific operational benefit or substantial lookup reduction. Keep the minimum fields, with a clear owner and refresh/invalidation lifecycle.
- The cache gateway may retain peer endpoints and keep-alive connections for repeated artifact streaming. Update/remove peers from NATS changes and reconcile after reconnect; this is not a second membership registry.

## Entry points and interfaces

| Component | Suggested location | Responsibility |
| --- | --- | --- |
| Entrypoint | `cmd/nixmox` | Parse runtime options and start the selected interface or daemon. |
| Daemon runtime | `internal/daemon` | Wire dependencies and manage service startup, readiness, and shutdown. |
| Domain models | `internal/domain` | Define shared records, typed inputs, and validation. |
| HTTP API | `internal/api` | Validate requests, dispatch operations, and serve paged results and SSE. |
| API client | `internal/client` | Share HTTP requests, error handling, and stream reconnection across interfaces. |
| CLI | `internal/cli` | Map commands and flags to client calls. |
| TUI/browser | `internal/ui` | Present records, forms, task reviews, and live updates. |

## Cluster services

| Component | Suggested location | Responsibility |
| --- | --- | --- |
| Local identity | `internal/identity` | Generate and persist node identity, certificates, and resume state. |
| NATS transport | `internal/cluster` | Manage TLS clients, embedded servers, subscriptions, and permissions. |
| Shared repositories | `internal/cluster` | Query JetStream records directly; provide revision checks and event-driven watches without mirrored registries. |
| Membership coordinator | `internal/cluster` | Serialize and checkpoint promotion, demotion, and removal. |
| Quorum coordinator | `internal/cluster` | Reconcile bootstrap ownership, replicas, and metadata peers. |
| Disaster recovery | `internal/cluster` | Execute local administrator-directed metadata rescue. |
| Discovery | `internal/enrollment` | Broadcast identity hints and publish observer-tagged discoveries. |
| Enrollment | `internal/enrollment` | Coordinate approval, CSR exchange, issuance, and rejection records. |
| Endpoint manager | `internal/network` | Publish interface addresses and reconcile preferred connection candidates. |
| Local networking | `internal/network` | Scan and configure installer network connections. |
| Credential store | `internal/credentials` | Encrypt/decrypt SOPS documents and reconcile recipient changes. |

## Tasks and host operations

| Component | Suggested location | Responsibility |
| --- | --- | --- |
| Task manager | `internal/task` | Dispatch owned work and manage dependencies, cancellation, retries, and reviews. |
| Resource coordinator | `internal/task` | Enforce host and cluster exclusivity across execution and reconnects. |
| Task persistence | `internal/task` | Save execution checkpoints, outputs, and parent/child/retry links. |
| Task reconciliation | `internal/task` | Resume interrupted transitions and mark orphaned work unknown. |
| Build runner | `internal/host` | Run pinned Nix builds and record output store paths. |
| Deployment runner | `internal/host` | Coordinate build, package diff, approval, and activation. |
| Rollback runner | `internal/host` | Activate the previous retained generation. |
| Power runner | `internal/host` | Perform preflight, request power/restart operations, and validate return. |
| Host observations | `internal/host` | Collect disks, hardware configuration, and public host keys. |

## Sources and recipes

| Component | Suggested location | Responsibility |
| --- | --- | --- |
| Source manager | `internal/source` | Track repositories, fetch revisions, and create pinned worktrees. |
| Configuration registry | `internal/source` | Discover and register deployable NixOS configurations. |
| Recipe versioning | `internal/recipe` | Preserve immutable recipe versions and source provenance. |
| Recipe resolver | `internal/recipe` | Resolve field/observation dependencies and invalidate stale values. |
| Recipe renderer | `internal/recipe` | Render templates, raw files, and SOPS updates into worktrees. |
| Publication runner | `internal/recipe` | Render onto the target revision and publish a commit or pull request. |

## Installation

| Component | Suggested location | Responsibility |
| --- | --- | --- |
| Installer registry | `internal/install` | Resolve installer definitions and their pinned build inputs. |
| Method registry | `internal/install` | Select built-in prepare/finalize methods by compatibility and preference. |
| Launch runner | `internal/install` | Coordinate installer build, transfer, demotion, and prepare execution. |
| Install-prepare methods | `internal/install` | Stage installer boot files, preserve private handoff state, and execute reboot/kexec. |
| Fresh-deployment runner | `internal/install` | Coordinate system build, disk formatting, installation, and completion checks. |
| Install-finalize methods | `internal/install` | Write destination identity/network state and configure the installed boot entry. |
| Media runner | `internal/install` | Transfer and write an installer artifact to the selected local drive. |

## Cache and observability

| Component | Suggested location | Responsibility |
| --- | --- | --- |
| Local binary cache | `internal/cache` | Serve signed Nix artifacts from the local store. |
| Cache gateway | `internal/cache` | Retain peer endpoints/keep-alive connections for streaming, with bounded timeouts and failover. |
| Telemetry integration | `internal/util` | Configure OpenTelemetry providers, propagators, and exporters for application-wide spans, metrics, and correlated, redacted logs. |
| Host collectors | `internal/host` | Publish host usage and inventory metrics. |
| Task log adapters | `internal/task` | Derive progress from process logs without interrupting raw capture. |
| Log storage/replay | `internal/cluster` | Retain logs and bridge historical replay into live delivery. |
| Health reporting | `internal/daemon` | Combine daemon readiness and local NATS connection checks. |

Keep these as focused Go components, not separate services or a custom plugin framework. Split packages only when their implementation needs a distinct dependency boundary.
