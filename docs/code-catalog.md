# Non-UI code catalog

Inventory of the working tree as of 2026-10-30, including uncommitted files. Covers all 51 non-UI Go files (35 implementation/support files and 16 test files), plus build and dependency configuration. Excludes `internal/ui/` and the browser entrypoint `cmd/nixmox/main_js.go`, generated artifacts, and dependencies outside this repository.

This is an inventory, **not a completion claim**. [Implementation components](implementation-components.md) describes intended boundaries; [requirements](requirements.md) and [CLI/API reference](cli-api-reference.md) describe the target behavior. Some CLI commands have no backend implementation. Cluster creation/cold restart and recipe/configuration service wiring are integration-tested; membership/enrollment and several other workflows remain unimplemented.

Function names below include private helpers, grouped where they share a responsibility. Methods are qualified by their receiver type; generic type arguments are omitted. Inline route/command callbacks are described under the function that registers them.

## Where new code belongs

| Concern | Existing owner / reuse point | Boundary to preserve |
| --- | --- | --- |
| Process lifetime and dependency wiring | `daemon.Runtime`, `daemon.Run` | Wire services here; keep business operations in their owning packages. |
| HTTP and CLI adapters | `api.Server`, `cli.NewCommand`, `client.Client` | Validate/map requests; do not implement another task engine or transport in an interface. |
| Shared record persistence | `cluster.Store` | Use native JetStream KV revisions and watches, not a mirrored registry. |
| Shared wire models | `domain` plus owner-specific models in `source`, `recipe`, `credentials`, `host` | Reuse existing records instead of introducing parallel API/client definitions. |
| Identity and trust | `identity.Node`, `identity.Authority` | Keep certificate issuance/verification and private file persistence centralized. |
| Credential encryption | `credentials.Cipher` | Use SOPS and SSH-derived age identity; recipient reconciliation is still missing. |
| Task state and exclusive resources | `task.Manager`, `task.Execution`, `task.Resources` | Add runners rather than independent job queues, review state machines, or lock systems. |
| Local subprocesses and host actions | `host.Process`, `host.Builder` | Reuse cancellation/output bounds; remote dispatch is not implemented by these helpers. |
| Git worktrees and pinned sources | `source.Manager`, `source.Worktree`, `source.Registry` | Reuse go-git and pin validation; refresh configurations explicitly. |
| Recipe inputs, versions and rendering | `recipe.Versions`, `recipe.Collector`, `recipe.Render` | Reuse dependency resolution and Go templates; publication remains unimplemented. |
| Nix cache formats and streaming | `cache.Local`, `cache.Gateway` | Use go-nix for Nix formats/signatures, not handwritten parsers or NAR encoders. |
| Telemetry and retained logs | `util`, `cluster.Logs`, cluster trace helpers | Provider/export lifecycle differs from retained-log persistence; do not duplicate either. |
| Integration infrastructure | `testutil.JetStream` | Reuse the TLS JetStream fixture; keep experimental topology tests in `poc`. |

There are no implementation packages yet for `internal/enrollment`, `internal/network`, or `internal/install`. Membership/quorum reconciliation, deployment/rollback/power workflows, publication, credential-store reconciliation, and distributed dispatch remain missing or incomplete. Extend the intended owner in the component plan rather than treating CLI command registration as implementation.

## Entrypoint and runtime

### [cmd/nixmox/main.go](../cmd/nixmox/main.go)

Native executable entrypoint (`!js`). `main` selects Gin release mode, creates a signal-cancelled context, calls the CLI, and exits with its status.

### [internal/daemon/runtime.go](../internal/daemon/runtime.go)

Process composition root. `Config` holds addresses, ports, state directory and SSH host-key settings; `startup` persists bootstrap inputs; `Runtime` owns identity, connections, servers, cache and workers.

- `Open`: open/lock node identity and restore saved cluster startup state; an unjoined node does not start NATS.
- `Runtime.Handler`: construct the HTTP adapter; `Runtime.Services`: expose connected cluster services; `Runtime.Health`: report local runtime/NATS status.
- `Runtime.CreateCluster`: create or retry bootstrap inputs, derive the age recipient, issue a node certificate and encrypt CA/cache signing credentials.
- `Runtime.saveStartup`: persist bootstrap state privately; `Runtime.start`: start embedded peers, connect/provision JetStream, seed records and wire the currently available services/build runner.
- `configureAccounts`: configure independent APP/SYS NATS accounts; `routePorts`: resolve configured/disposable route ports; `strconvPort`: format a port.
- `Runtime.Close`: stop workers, connections, embedded servers and identity ownership; `Run`: own the HTTP listener, runtime failures and graceful shutdown.

**Status:** main/bootstrap start concurrently; bucket-open reads retry bounded request timeouts while restored stream leaders elect. Configuration/recipe stores, registry, and available observation collectors are wired. Host disks/hardware callbacks and membership/enrollment coordination remain unimplemented.

## Shared domain records

### [internal/domain/cluster.go](../internal/domain/cluster.go)

Data-only wire records: `Cluster`, `ClusterCA`, `Member`, `DaemonInfo`, `Endpoint`, `Health`, and `Check`. No functions. Owns cluster identity, membership/endpoint metadata and health response shapes.

### [internal/domain/task.go](../internal/domain/task.go)

Task statuses and records: `Task`, `TaskLinks`, `ChildTask`, `OutputRef`, `TaskConcurrency`, `Review`, `TaskError`, and `TaskOutput`. `TaskStatus.Terminal` identifies completed/non-resumable statuses. Execution behavior belongs in `task`, not these models.

## Identity and credentials

### [internal/identity/identity.go](../internal/identity/identity.go)

Persistent node identity and certificate primitives. `State` is persisted metadata; `Node` holds the identity key and process lock; `Authority` holds CA material.

- `Open`: load/create and exclusively lock node state; `Node.Save`: persist metadata; `Node.Close`: release ownership.
- `WritePrivate`: atomically write private files with restrictive permissions.
- `Node.CSR`: create a signing request; `Thumbprint`: fingerprint a public key; `ParseCertificate`: decode a PEM certificate.
- `serial`: generate random certificate serials; `NewAuthority`: create a cluster CA.
- `Authority.Issue`: validate the CSR/expected identity and issue a node certificate with supplied names.
- `Node.VerifyEnrollment`: verify issued certificate trust and its binding to this node's key/identity.

### [internal/credentials/cipher.go](../internal/credentials/cipher.go)

SOPS encryption primitives and credential/recipient records (`Recipient`, `Recipients`, `Record`). `MaxSecretSize` limits plaintext; `Cipher` names the local age identity file.

- `DeriveIdentity`: derive age identity/public recipient from an actual SSH host key using `ssh-to-age`, persisting the identity privately.
- `recipientKeys`: validate and normalize recipient keys; `Cipher.run`: invoke SOPS with isolated configuration and bounded execution/output.
- `Cipher.Encrypt`: encrypt a bounded secret for recipients; `Cipher.Decrypt`: recover its plaintext; `Cipher.Rekey`: re-encrypt for an updated recipient set.

Does not yet implement shared credential CRUD or membership-driven recipient reconciliation. Recipe `.sops.yaml` editing is a separate concern in `recipe/sops.go`.

## Cluster transport, storage and logs

### [internal/cluster/transport.go](../internal/cluster/transport.go)

NATS/TLS connection primitives and OpenTelemetry propagation.

- `TLSConfig`: build mutual-TLS configuration; `Connect`: establish a NATS client; `NewServer`: validate and construct an embedded server without starting it; `StartServer`: start a single embedded server and wait for its listeners.
- `headerCarrier.Get`, `headerCarrier.Set`, `headerCarrier.Keys`: adapt NATS headers to the OTel carrier interface.
- `Inject`, `Extract`: propagate trace context through messages; `Request`: perform a traced request/reply.
- `DispatchContext`: serialize tracing for durable work; `ResumeContext`: restore it onto the worker lifetime rather than the initiating HTTP request.

### [internal/cluster/store.go](../internal/cluster/store.go)

Generic JSON records over native JetStream KV. `Record` contains key, value and revision; `Store` wraps the KV handle.

- `OpenStore`: open an existing bucket; `decode`: convert a KV entry into a typed record.
- `Store.Get`, `Store.Create`: read or create-only write a record.
- `Store.Update`, `Store.Delete`: require revision-checked mutations.
- `Store.List`: return a sorted cursor page; `Store.Watch`: deliver initial snapshot, snapshot completion and subsequent native KV changes.

### [internal/cluster/quorum.go](../internal/cluster/quorum.go)

Pure topology rules, not a live reconciler. `Topology` describes the plan. `Plan` computes bootstrap/replica requirements from the stable-host count; `ValidateTransition` checks membership transitions against those constraints.

### [internal/cluster/logs.go](../internal/cluster/logs.go)

Retained OTLP host/task logs over JetStream. `Logs` owns the handle; `LogEntry` carries sequence and decoded data; `LogRetention` is five days.

- `LogSubject`: validate identifiers and build host/task subjects; `Logs.Provision`: create/update the retained stream.
- `Logs.Append`: persist an OTLP log batch with tracing.
- `Logs.Follow`: replay after a sequence and continue live with an ordered consumer; `Logs.Read`: return a bounded historical batch.

## Durable tasks

### [internal/task/manager.go](../internal/task/manager.go)

Owned-host scheduling and persistent task transitions. `Runner` is the operation callback; `Manager` owns stores/runner handles; `Execution` exposes checkpoint/review operations; `ErrReview` pauses a runner.

- `Manager.Submit`: initialize/persist queued work; `Manager.Retry`: create a new task linked to a terminal predecessor.
- `Manager.owned`: enforce local ownership; `Manager.Cancel`: cancel the local runner or finish inactive work; `Manager.Approve`: approve the exact pending output.
- `Manager.Run`: reconcile then consume task/resource watches; `Manager.each`: visit paged tasks.
- `Manager.reconcile`: mark interrupted running work unknown and release terminal-task locks.
- `Manager.schedule`: check ownership, dependencies, review approval and resources before starting runners.
- `Manager.execute`: own runner lifetime, panic/cancellation handling and review/completion transitions; `Manager.finish`: persist terminal result/error before releasing resources.
- `Execution.Task`: expose the current record; `Execution.Checkpoint`: persist a named JSON checkpoint; `Execution.save`: revision-check an execution update.
- `Execution.Review`: create immutable structured output and pause until approval, or recognize an already-approved review on re-entry.

This is not yet cluster-wide remote dispatch or a complete workflow recovery coordinator.

### [internal/task/resources.go](../internal/task/resources.go)

CAS-based exclusive-resource ownership with no TTL. `Resources` wraps the shared ownership record; `ErrBusy` means another task holds a requested resource.

- `Resources.Acquire`: atomically acquire the requested set; `Resources.Release`: release all ownership for a task.
- `Resources.change`: apply a mutation through a revision-checked retry loop.

## Sources and configuration registry

### [internal/source/source.go](../internal/source/source.go)

Repository metadata (`Repository`), immutable revision inputs (`Pin`), deployable `Configuration` records, and temporary pinned go-git worktrees.

- `Pin.Validate`: require a repository identity, reference and full commit SHA.
- `Manager.clone`: clone into an isolated working directory; `Manager.Refresh`: resolve the tracked reference to its current commit; `Manager.Checkout`: produce a worktree at the requested pin.
- `Worktree.Close`: delete temporary working data; `Worktree.ReadFile`: read a bounded repository-relative file safely; `Worktree.Directory`: resolve a safe existing subdirectory.

Private-repository credential integration is not complete; extend this owner rather than cloning separately in build/render code.

### [internal/source/registry.go](../internal/source/registry.go)

`Registry` discovers NixOS configuration names using pinned worktrees, shared stores and an injected process runner.

- `Registry.names`: evaluate `configurations.nixos` attribute names with Nix, without changing lock files.
- `Registry.Register`: create deterministic configuration records while preserving existing pins.
- `Registry.Refresh`: explicitly resolve a newer revision, verify the named configuration still exists, and CAS-update its pin.

## Local host execution

### [internal/host/process.go](../internal/host/process.go)

Bounded subprocess runner, shared by builds and observations.

- `Process.Run`: run a command with input, caller-context cancellation/deadlines, process-group cleanup, bounded stdout capture and an optional stderr sink.
- `boundedOutput.Write`: enforce stdout capture bounds and cancel execution on overflow.

### [internal/host/build.go](../internal/host/build.go)

Pinned Nix builds (`Builder`, `BuildInput`, `BuildResult`) and local disk/hardware observations.

- `Builder.Run`: resolve/check out pinned sources and invoke Nix with cache settings.
- `ParseBuildResult`: decode Nix build JSON and validate store paths using go-nix.
- `Builder.Runner`: adapt task JSON inputs to the build operation.
- `Process.Disks`: collect disk information with `lsblk`; `Process.Hardware`: generate hardware configuration without filesystem declarations.

Deployment, rollback, power operations and periodic metrics publishing are not implemented here yet.

## Recipe pipeline

### [internal/recipe/model.go](../internal/recipe/model.go)

Recipe definitions and normalized execution state: `Field`, `Observation`, `SOPSUpdate`, `Output`, `Definition`, `Source`, `CollectedObservation`, `Collected`, and `Record`.

- `clone`: deep-copy JSON-shaped values; `object`: convert values to a JSON object; `keys`: return sorted map keys.
- `Record.inputs`: assemble template inputs; `observationInput`: evaluate observation parameters and identify their effective inputs.
- `Record.Normalize`: validate definitions/pins, resolve dependency readiness, invalidate stale values and compute renderability.
- `Field.accepts`: check field values against type/selection constraints.
- `Collect`: refresh requested observations in dependency order through an injected collector and normalize the resulting record.

### [internal/recipe/dependencies.go](../internal/recipe/dependencies.go)

Dependency analysis for fields and observations (`input`, `dependencyNode`).

- `input.key`: name a dependency; `Definition.dependencies`: inspect template syntax/nested values to find referenced inputs.
- `Definition.order`: validate references and topologically order nodes, rejecting cycles.
- `Record.ready`: check whether a dependency node has its required inputs.

### [internal/recipe/template.go](../internal/recipe/template.go)

Go-template evaluation shared by normalization, observations and rendering. `RenderLimit` is 1 MiB.

- `boundedText.Write`: limit rendered text; `parseTemplate`: parse with strict missing-key behavior; `singleAction`: recognize a standalone template action.
- `Text`: render bounded text; `Evaluate`: recursively evaluate JSON-shaped values while preserving typed results from standalone actions.

### [internal/recipe/versions.go](../internal/recipe/versions.go)

Immutable shared recipe history (`Versions`) and editable inputs (`Patch`).

- `versionKey`: validate/build an immutable version key; `Versions.Get`: read an exact version.
- `Versions.List`: page latest versions by recipe; `Versions.Latest`: discover the latest version through a native KV snapshot, without a mutable head mirror.
- `Versions.Create`: assign a new recipe ID/version; `Versions.persist`: normalize, bound and create-only persist a version.
- `Versions.current`: require the expected latest version; `Versions.Update`: create a new version, detaching a changed Git-sourced definition into a new recipe.
- `Versions.Observe`: collect observations and persist the next immutable version.

### [internal/recipe/observe.go](../internal/recipe/observe.go)

Built-in observation adapters. `Collector` receives stores, worktrees, process execution and host-service callbacks.

- `Collector.Collect`: collect cluster hosts/repositories/credential IDs, host disks/hardware/age keys, repository files or operator-authorized repository commands.
- `all`: collect paged records without a persistent mirror.
- `Collector.repository`: resolve/register a referenced repository and produce a validated pin.

Host callbacks must route to the correct host; missing remote services are not replaced by local execution.

### [internal/recipe/render.go](../internal/recipe/render.go)

Render a normalized recipe into its supplied pinned worktree. `Rendered` returns changed file names and the selected flake configuration.

- `Render`: validate readiness/pin/path constraints, prepare template/raw/SOPS outputs, then write them in deterministic order.
- `safeOutput`: reject escaping paths, Git metadata writes and symlink traversal; `readOutput`: bounded read for updating an existing output.

The caller must discard the worktree after failure. Rendering does not commit, push or open a pull request.

### [internal/recipe/sops.go](../internal/recipe/sops.go)

YAML-node editing of `.sops.yaml` recipient configuration; not secret encryption.

- `UpdateSOPS`: add/update a named age anchor and selected creation-rule key groups while retaining YAML structure.
- `scalar`: construct a scalar node; `mappingValue`, `setMapping`: read/update mapping entries.
- `findAnchor`: find an existing anchor; `addAge`: add an age alias to a key group without duplicating it.

## Nix binary cache

### [internal/cache/local.go](../internal/cache/local.go)

Signed HTTP binary cache backed by the local Nix store (`Local`). Uses go-nix for NAR encoding, narinfo, signatures and store-path handling.

- `NewLocal`: parse signing material and configure process execution; `Local.PublicKey`: expose the cache verification key.
- `Local.Metadata`: query authoritative Nix store hashes/sizes/references, validate metadata and sign narinfo.
- `Local.ServeHTTP`: serve cache information, narinfo and NAR content.
- `contextWriter.Write`: stop streaming writes when the request is cancelled.

### [internal/cache/gateway.go](../internal/cache/gateway.go)

Streaming peer-cache proxy. `Peer` describes an endpoint; `Gateway` retains only routing/connection state needed for repeated streaming.

- `NewGateway`: create a bounded keep-alive HTTP transport; `Gateway.Close`: close idle connections.
- `Gateway.ReplacePeers`: validate and replace peer endpoints; `Gateway.ServeHTTP`: select a peer and fail over before response streaming begins.
- `idleConn.Read`: refresh read-idle deadlines during transfer.

Membership-driven peer refresh/reconnect reconciliation remains a runtime integration concern.

## HTTP API

### [internal/api/server.go](../internal/api/server.go)

Thin Gin adapters. `Services` carries injected cluster/task/source/recipe/cache dependencies; `Server` supplies runtime health, cluster creation and service lookup.

- `Server.Handler`: register instrumentation, request bounds, error recovery, health/cluster routes, host/repository reads, repository create/refresh, local build submission, task mutation/output/events, retained log reads/streams and cache forwarding; register source routes.
- `services`: retrieve request-scoped service handles; `body`: strictly decode bounded JSON; `fail`: map service errors to HTTP responses.
- `respondRecord`: return a typed record value; `list`: adapt cursor-paged stores; `sse`: frame/flush bounded-time event writes.

Routes are partial. Remote build dispatch is explicitly unavailable; this adapter is not the complete API contract.

### [internal/api/sources.go](../internal/api/sources.go)

`registerSourceRoutes` registers configuration list/show/refresh and recipe list/create/version-read/update/observe endpoints. Adapters use `source.Registry`, `recipe.Versions` and `recipe.Collector`; unavailable services return service-unavailable responses. Recipe render/publication routes are not implemented here.

## Shared HTTP client

### [internal/client/client.go](../internal/client/client.go)

HTTP JSON client shared by interfaces. `Client` owns endpoint/transport; `Page` is a paged response; `Error` is an API failure; `UnknownSubmission` signals an uncertain mutation outcome. `ResponseLimit` bounds responses.

- `New`: validate endpoint and configure traced HTTP transport; `Client.Close`: close idle connections.
- `Client.request`: construct contextual requests; `Client.Do`: encode input, issue a request and decode bounded output without silently retrying mutations.
- `incomplete`: classify uncertain results; `responseError`: decode an HTTP failure.
- `Error.Error`, `UnknownSubmission.Error`: format failures; `UnknownSubmission.Unwrap`: expose the underlying cause.

### [internal/client/stream.go](../internal/client/stream.go)

Reconnecting SSE client. `Event` carries ID/type/data; `callbackError` and `protocolError` distinguish failures that should stop delivery.

- `Client.Watch`: reconnect streams with cursor/retry state and cancellation.
- `readEvents`: parse bounded SSE frames, IDs and retry hints, dispatching completed events to the callback.

## CLI adapters

### [internal/cli/cli.go](../internal/cli/cli.go)

Cobra/Viper command setup and common I/O. `console` holds CLI configuration, not shared application state.

- `Execute`: initialize telemetry, execute commands and choose an exit status; `NewCommand`: construct the command tree and bind configuration/flags/I/O.
- `console.daemonCommand`: map daemon flags into runtime configuration.
- `console.call`: perform/print a JSON client request; `console.watch`: print streamed events.
- `definition`: read a bounded JSON/YAML definition; `required`: add a required string flag; `flag`, `boolean`: read flag values.

### [internal/cli/read.go](../internal/cli/read.go)

Read-side command registration.

- `console.get`: construct an escaped GET command with paging/filter flags.
- `console.addReads`: register resource list/show, logs, metrics, task watch/output, versioned recipe reads and Wi-Fi scan commands.
- `child`: find a known Cobra subgroup during assembly (not a second runtime dispatcher).

### [internal/cli/mutate.go](../internal/cli/mutate.go)

Mutation command registration and safe secret input.

- `console.addMutations`: register cluster/membership/repository/configuration/enrollment/task actions and the specialized groups below.
- `versionFlag`: require a version; `recipePath`: validate/build an immutable version URL; `console.addRecipes`: map create/update/observe/render/publish commands.
- `console.addHostJobs`: map build/deploy/install/rollback/installer/media commands.
- `secretInput`: read bounded secret bytes from stdin or a hidden terminal prompt; `console.addSecrets`: register credential put and Wi-Fi connect without password command arguments.

Command availability does not prove server support. Keep new command code as mappings to services, not embedded implementations of missing workflows.

## Logging and telemetry

### [internal/util/logging.go](../internal/util/logging.go)

Context-correlated structured logging and bounded redaction.

- `base`: choose the context/default logger; `From`: bind current span correlation without retaining request lifetime; `With`: attach operation attributes.
- `Configure`: install local JSON/OTel fan-out and redaction, returning a local-only exporter-error logger.
- `clean`: redact configured secrets and truncate text; `Error`: sanitize errors; `cleanAttr`: sanitize sensitive/nested attributes.
- `safeHandler.Handle`: sanitize records and add trace/span IDs; `safeHandler.WithAttrs`, `safeHandler.WithGroup`: preserve sanitization when deriving handlers.
- `contextHandler.Enabled`, `contextHandler.Handle`: delegate using the stored correlation context; `contextHandler.WithAttrs`, `contextHandler.WithGroup`: preserve that context on derived handlers.

### [internal/util/otel.go](../internal/util/otel.go)

Process-owned OpenTelemetry setup. `Config` identifies the service/instance, optional collector and output. `Start` installs trace/metric/log providers, W3C propagation and logging; optionally creates OTLP/HTTP exporters; returns a shutdown function that flushes owned providers.

## Test support and executable examples

Tests describe intended coverage, not a statement that the entire suite is green. `test-backend` excludes daemon integration, `util`, PoC and UI tests. Cluster lifecycle passes through `test-runtime`; legacy UI compilation and a logging example output mismatch still prevent a green full suite.

### [internal/testutil/nats.go](../internal/testutil/nats.go)

`JetStream` creates a disposable TLS-enabled embedded NATS/JetStream instance and registers cleanup for integration tests. Reuse this fixture for shared-store tests; it does not validate production multi-peer bootstrap.

| File | Functions and purpose |
| --- | --- |
| [internal/identity/identity_test.go](../internal/identity/identity_test.go) | `ExampleNode`: persistent identity, exclusive ownership and certificate enrollment usage. |
| [internal/cluster/logs_test.go](../internal/cluster/logs_test.go) | `TestLogReplayThenLive`: historical OTLP replay followed by live delivery. |
| [internal/task/manager_test.go](../internal/task/manager_test.go) | `TestReviewAndRetry`: durable task execution, review/approval and retry behavior over JetStream. |
| [internal/source/source_test.go](../internal/source/source_test.go) | `TestPinnedWorktree`: pinned Git checkout and bounded/safe worktree access. |
| [internal/source/registry_test.go](../internal/source/registry_test.go) | `TestConfigurationRegistry`: discover configurations, preserve pins and explicitly refresh; real Nix path enabled by `NIXMOX_TEST_NIX=1`. |
| [internal/host/build_test.go](../internal/host/build_test.go) | `ExampleParseBuildResult`: deterministic Nix build-result/store-path parsing example. |
| [internal/cache/cache_test.go](../internal/cache/cache_test.go) | `TestSignedCache`: signed metadata/NAR serving and gateway behavior; optional real `nix copy` with `NIXMOX_TEST_STORE_PATH`. |
| [internal/credentials/cipher_test.go](../internal/credentials/cipher_test.go) | `TestSOPSRecipients`: SSH-derived identities and SOPS encryption/rekey behavior when recipients are added/removed. |
| [internal/client/client_test.go](../internal/client/client_test.go) | `TestTraceAcrossHTTPAndNATS`: trace continuity across transports; `TestSSEReconnect`: cursor-based reconnect behavior. |
| [internal/cli/cli_test.go](../internal/cli/cli_test.go) | `ExampleNewCommand`: command usage; `TestCredentialInputAndReview`: secret input and task approval payload binding. |
| [internal/recipe/recipe_test.go](../internal/recipe/recipe_test.go) | `TestRecipeCollectVersionAndRender`: collection, immutable versions, invalidation and safe rendering; `ExampleUpdateSOPS`: YAML recipient update usage. |
| [internal/api/sources_test.go](../internal/api/sources_test.go) | `TestRecipeAPI`: recipe/configuration service adapters using injected test services. |
| [internal/daemon/runtime_test.go](../internal/daemon/runtime_test.go) | `TestClusterLifecycle`: cluster creation, recipe creation/observation through HTTP, configuration endpoint availability, and identity/recipe persistence through cold restart. |
| [internal/util/logging_test.go](../internal/util/logging_test.go) | `ExampleConfigure`: correlated/redacted logging; `ExampleError`: sanitized errors. |
| [internal/util/otel_test.go](../internal/util/otel_test.go) | `ExampleStart`: process telemetry setup, correlation and shutdown, restoring global state. |
| [poc/jetstream_bootstrap_test.go](../poc/jetstream_bootstrap_test.go) | `TestBootstrapPeer`: experimental embedded JetStream bootstrap topology; not production runtime wiring. |

## Build, environment and dependency files

| File | Contents / executable responsibilities |
| --- | --- |
| [flake.nix](../flake.nix) | Nix package/devshell for x86_64/aarch64 Linux. `outputs` and local `each` produce per-system packages, tooling and formatter; `buildGoModule` builds/checks the application. Also contains existing browser asset build/install hooks (UI implementation excluded from this catalog). Commented module exports are not implementations. |
| [flake.lock](../flake.lock) | Pins the Nix input graph; no application functions. |
| [.envrc](../.envrc) | `use flake` activates the devshell through direnv. |
| [go.mod](../go.mod) | Go module/toolchain requirements and direct/indirect dependency declarations. |
| [go.sum](../go.sum) | Go dependency checksums; no application functions. |
| [justfile](../justfile) | Developer build, run, test and smoke recipes listed below. |

Run recipes inside the devshell, e.g. `nix develop -c just test-backend`:

- `test`: entire Go suite, including currently unresolved integrations.
- `test-backend`: isolated identity/cluster/task/source/host/cache/credentials/client/CLI/recipe/API checks.
- `build`: native binary at `bin/nixmox`; `run`: execute native CLI arguments through Go.
- `smoke-unjoined`: start a temporary unjoined daemon, verify CLI health without NATS, and clean up.
- `test-registry`: enable real Nix configuration discovery.
- `test-cache`: create a Nix store fixture and enable real signed-cache copying.
- `test-logging`: logging/redaction examples; `test-otel`: telemetry lifecycle example.

## Keeping this useful

When adding, moving or removing a non-UI file, update its entry here and the relevant ownership row. Document new functions under the owning file, grouping small private helpers rather than copying signatures or bodies. Check the actual service wiring before changing an integration-status note. Keep planned features in the component plan until code exists, and prefer the reuse points above before adding another abstraction.
