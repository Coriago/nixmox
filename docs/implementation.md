# Implementation guide

Implementation ownership and code map for [requirements.md](requirements.md). Commands and routes are in [cli-api-reference.md](cli-api-reference.md).

## status
- cluster creation, cold restart, recipe/configuration lifecycles and the local build/log pipeline have integration coverage
- CLI command availability does not imply backend support
- enrollment, networking and installation packages are not implemented
- membership/quorum reconciliation, distributed dispatch, deployment, rollback, power operations, publication and credential-store reconciliation remain missing or incomplete
- the code map covers non-UI application code with selected UI integration points; functions are grouped by responsibility, not an exhaustive helper inventory
- terminal/browser interfaces share the HTTP client and build monitor; component tests do not establish a complete multi-host workflow

## libraries
- go-git: cloning, revision resolution and pinned checkouts; production commit/push publication is not implemented
- go-nix: store paths, NAR, narinfo, signatures, hashes and base32; derivation parsing is not currently used
- Gin: HTTP routing and middleware
- Cobra: commands, flags and help
- Viper: files, environment and flag bindings; not shared application state
- OpenTelemetry: traces, metrics and correlated logs

## ownership
These are implementation rules, not claims that every planned integration is complete.

- NATS owns shared records; query native JetStream KV with revision checks and watches, not mirrored registries or a local members list
- local state is limited to identity, startup/resume data, active runners, connections and operation working data
- retained shared data needs a specific benefit, minimal fields and an explicit refresh/invalidation lifecycle
- cache peers and keep-alive connections are streaming state, not a second membership registry; reconcile them after reconnect
- interfaces validate and map requests; business operations stay in their domain services
- reuse domain and owner-specific wire models rather than parallel API/client records
- add task runners, not independent queues, review state machines or lock systems
- keep components focused; split packages only for distinct dependency boundaries, not a custom plugin framework

## instrumentation
Tracing helpers and provider setup exist; these rules also apply to future workflows.

- propagate `context.Context` and W3C trace headers through CLI, HTTP, NATS and task phases
- use HTTP/Gin instrumentation and NATS injection/extraction for requests, replies and JetStream messages
- persist originating trace context with durable dispatch metadata; restore it on the worker lifetime
- asynchronous tasks outlive HTTP requests; use span links for retries/resumes where appropriate
- use stable operation names, span kinds, host/task attributes, error recording and status; never record secrets
- centralize providers, propagators, sampling and exporters; flush on CLI/daemon shutdown
- verify HTTP-to-NATS trace continuity in integration tests

## entrypoints and runtime
- [cmd/nixmox/main.go](../cmd/nixmox/main.go): native entrypoint; `main` sets Gin mode, creates a signal context and runs the CLI
- [internal/daemon/runtime.go](../internal/daemon/runtime.go): composition root; `Config` holds runtime settings, `startup` persists bootstrap inputs and `Runtime` owns services
  - `Open`: lock identity and restore startup state; unjoined nodes do not start NATS
  - `Handler`, `Services`, `Health`: expose HTTP wiring, connected services and readiness
  - `CreateCluster`: persist bootstrap inputs, derive the age recipient, issue certificates and encrypt CA/cache credentials
  - `saveStartup`, `start`: persist private state, start peers, provision stores and wire services/build workers
  - `configureAccounts`, `routePorts`, `strconvPort`: NATS account and port setup
  - `Close`, `Run`: service cleanup, HTTP lifetime and graceful shutdown
- main/bootstrap peers start concurrently; bounded bucket-open retries allow restored stream leaders to elect
- startup always constructs main/bootstrap peers on one host and provisions two replicas; topology planning is not wired to membership changes
- recipe/configuration services and Git-only refresh are wired; registry discovery has no runtime trigger
- cluster host/repository/credential-ID observations and member age-key lookup are wired; host disk/hardware callbacks are unset
- only the build runner is registered; enrollment and client-only member startup are not implemented

## shared records
- [internal/domain/cluster.go](../internal/domain/cluster.go): `Cluster`, `ClusterCA`, `Member`, `DaemonInfo`, `Endpoint`, `Health` and `Check`
- [internal/domain/task.go](../internal/domain/task.go): task states, links, concurrency, reviews, errors and outputs; `TaskStatus.Terminal` identifies terminal states
- records carry data; execution belongs to the owning service

## identity and credentials
- [internal/identity/identity.go](../internal/identity/identity.go): persistent `State`, locked `Node` and certificate `Authority`
  - `Open`, `Node.Save`, `Node.Close`: identity persistence and exclusive ownership
  - `WritePrivate`: atomic private-file writes; `serial`: certificate serial generation
  - `Node.CSR`, `Thumbprint`, `ParseCertificate`: certificate inputs and key fingerprints
  - `NewAuthority`, `Authority.Issue`, `Node.VerifyEnrollment`: CA creation, CSR validation and certificate/key/identity verification
- [internal/credentials/cipher.go](../internal/credentials/cipher.go): `Cipher`, credential/recipient records and bounded secrets
  - `DeriveIdentity`: SSH host key to private age identity and public recipient
  - `recipientKeys`, `Cipher.run`: recipient validation and isolated, bounded SOPS execution
  - `Encrypt`, `Decrypt`, `Rekey`: secret encryption and recipient rotation
- shared credential CRUD and membership-driven recipient reconciliation remain pending; recipe `.sops.yaml` editing is separate from encryption

## cluster transport and storage
- [internal/cluster/transport.go](../internal/cluster/transport.go): mutual TLS, embedded NATS and trace propagation
  - `TLSConfig`, `Connect`, `NewServer`, `StartServer`: transport construction and startup
  - `headerCarrier.Get/Set/Keys`, `Inject`, `Extract`, `Request`: traced NATS headers and request/reply
  - `DispatchContext`, `ResumeContext`: persisted trace context without retaining HTTP lifetime
- [internal/cluster/store.go](../internal/cluster/store.go): typed JSON `Record` and native KV `Store`
  - `OpenStore`, `decode`, `Get`, `Create`: open, decode, read and create-only writes
  - `Update`, `Delete`: revision-checked mutations
  - `List`, `Watch`: sorted cursor pages and native snapshot/change delivery
- [internal/cluster/versions.go](../internal/cluster/versions.go): `VersionKey` validates UUID/version keys; `VersionNumbers` scans latest versions and rejects unknown key formats
- [internal/cluster/quorum.go](../internal/cluster/quorum.go): pure `Topology`, `Plan` and `ValidateTransition` rules; not a live reconciler
- [internal/cluster/logs.go](../internal/cluster/logs.go): retained OTLP host/task logs with five-day retention
  - `LogSubject`, `Logs.Provision`, `Append`: subject validation, stream setup and traced writes
  - `Follow`, `Read`: ordered replay-to-live delivery and bounded history
- planned cluster coordination owns serialized membership transitions, bootstrap placement, replicas, metadata peers and administrator-directed quorum rescue

## durable tasks
- [internal/task/manager.go](../internal/task/manager.go): owned-host scheduling; `Runner`, `Manager` and `ErrReview`
  - `Submit`, `Retry`: persist queued work and linked retries
  - `owned`, `Cancel`, `Approve`: ownership, cancellation and exact-output review approval
  - `Run`, `each`, `reconcile`: consume watches, page tasks and mark interrupted running work unknown
  - `schedule`, `execute`, `finish`: dependencies, reviews, resources, runner lifetime and completion
- [internal/task/execution.go](../internal/task/execution.go): `Execution.Task/Checkpoint/Review/save` expose task state and persist checkpoints, outputs and reviews
- [internal/task/resources.go](../internal/task/resources.go): CAS-based `Resources` with no TTL; `Acquire`, `Release` and `change` manage task ownership
- [internal/task/log.go](../internal/task/log.go): `Execution.LogWriter`, `Write` and `Close` frame bounded stderr into retained OTLP logs; redact secrets and report persistence failures
- `Builder.Runner` attaches the log writer for replayable Nix activity; distributed dispatch and complete workflow recovery remain pending

## sources and configuration discovery
- [internal/source/source.go](../internal/source/source.go): `Repository`, immutable `Pin`, `Manager` and temporary go-git `Worktree`
  - `Pin.Validate`: repository identity, ref and full SHA
  - `Manager.clone`, `Refresh`, `Checkout`: isolated worktrees and explicit revision resolution
  - `Worktree.Close`, `ReadFile`, `Directory`: cleanup and bounded, safe repository access
- [internal/configuration/registry.go](../internal/configuration/registry.go): pinned NixOS configuration discovery
  - `Registry.names`: evaluate `configurations.nixos` without changing lock files
  - `Register`: deterministic records preserving existing pins
  - `Refresh`: validate the named configuration and append a version at the new revision
- `Registry.Register` is called by tests only; repository add/refresh does not discover configurations, and `.nixmox/recipes` scanning is absent
- repository records and pins have no `rootDir`; paths currently resolve from the Git worktree root
- `source.Manager.Auth` exists but the daemon leaves it unset, so repositories with `credentialId` fail as unavailable
- build/render code must reuse source ownership rather than clone independently

## recipes and configurations
- recipes own reusable definitions; configurations own collected realizations and exact recipe references
- stored versions use schema 2; legacy records are rejected, not silently migrated or erased

**Recipe definitions and rendering**<br>
Location: `internal/recipe`
- [model.go](../internal/recipe/model.go): `Version`, `Definition`, fields, observations, outputs, provenance and references; `ObservationType` names built-ins and `Canonical` normalizes JSON shapes
- [fields.go](../internal/recipe/fields.go): `Field.Validate`, `validateSelection`, `matchesChoice`; invalid choice expressions remain input errors
- [errors.go](../internal/recipe/errors.go): `ErrInvalid` and `Invalid` distinguish invalid recipe/configuration inputs for HTTP error mapping
- [service.go](../internal/recipe/service.go): `Service.Create/Import/Edit/Sync`; imports verify pinned files and sync compares normalized definitions
- [repository.go](../internal/recipe/repository.go): `Repository.Get/Latest/List`, expected-version checks in `current`, create-only writes in `save`
- [dependencies.go](../internal/recipe/dependencies.go): `Definition.Order` detects cycles and orders template-derived dependencies without external operations
- [template.go](../internal/recipe/template.go): bounded Go templates via `Text` and `Evaluate`; whole-value expressions preserve types
- [render.go](../internal/recipe/render.go): `Render` validates paths and delegates template/raw/SOPS outputs in a caller-owned pinned worktree; returns bounded contents without collecting or publishing
- [sops.go](../internal/recipe/sops.go): `UpdateSOPS` preserves YAML structure while updating age anchors and key groups; `scalar`, `mappingValue`, `setMapping`, `findAnchor`, `addAge` handle node edits
- changed tracked recipes fork their identity while retaining pinned template dependencies; changed detached recipes append versions; unchanged edits/sync return the existing version
- sync never upgrades configurations automatically

**Configuration realizations**<br>
Location: `internal/configuration`
- [model.go](../internal/configuration/model.go): versioned Git configurations or recipe realizations, fields and observation evidence; `Create` accepts user inputs, not forged observations
- [service.go](../internal/configuration/service.go): `Service.Create/SetInputs/UpgradeRecipe/RefreshTarget`; `loadRecipe` retrieves the exact reference
- [repository.go](../internal/configuration/repository.go): immutable persistence and expected-version checks
- [inputs.go](../internal/configuration/inputs.go): `templateInputs`, `dependenciesReady`, `invalidateStaleInputs`, `validateSubmitted`; pure processing, no Git refresh or collection
- [observe.go](../internal/configuration/observe.go): `Service.Observe` owns ordered collection, fingerprints, timestamps, invalidation and persistence; `resolveObservation` fingerprints original/resolved definitions
- [collector.go](../internal/configuration/collector.go): `Collector.Collect` dispatches fixed built-ins
  - `collectClusterHosts/Repos/Credentials`: cluster metadata
  - `collectHostDisks/Hardware/AgeKey`: supplied host callbacks, never a local fallback
  - `collectRepositoryFile/Command`: pinned worktrees and bounded outputs
  - `checkoutObservation`, `repository`, `listAll`: source resolution and paged reads
- [render.go](../internal/configuration/render.go): `Service.Render` requires a recipe-backed configuration, loads exact versions and checks readiness; owns a fresh worktree and returns files before cleanup without recollecting
- upgrades affect only the selected configuration; publication of an exact version by commit or pull request remains pending

## host execution
- [internal/host/process.go](../internal/host/process.go): `Process.Run` handles input, caller deadlines, process-group cancellation, bounded stdout and an optional stderr sink; `boundedOutput.Write` cancels stdout overflow
- [internal/host/build.go](../internal/host/build.go): `Builder.Run` builds pinned sources with optional cache settings; `ParseBuildResult` validates paths through go-nix; `Builder.Runner` adapts task inputs
- the daemon leaves `CacheURL` and `CachePublicKey` unset, so builds do not automatically use the cluster cache
- the source commit is pinned, but the build command does not disable flake lock updates
- [internal/host/inspect.go](../internal/host/inspect.go): `Process.Disks` uses `lsblk`; `Process.Hardware` generates hardware configuration without filesystem declarations
- local helpers do not dispatch remote work
- planned runners own deployment build/diff/review/activation, previous-generation rollback, power/restart preflight and return validation; periodic host metrics remain pending

## binary cache
- [internal/cache/local.go](../internal/cache/local.go): signed HTTP cache backed by the local Nix store and go-nix formats
  - `NewLocal`, `Local.PublicKey`: signing setup and public key
  - `Local.Metadata`: authoritative store metadata and signed narinfo
  - `Local.ServeHTTP`, `contextWriter.Write`: cache/NAR delivery and cancellation
- [internal/cache/gateway.go](../internal/cache/gateway.go): bounded streaming peer proxy
  - `NewGateway`, `Gateway.Close`: keep-alive transport lifetime
  - `ReplacePeers`, `ServeHTTP`: validated endpoints and failover before streaming begins
  - `idleConn.Read`: read-idle deadline renewal
- runtime seeds only its own advertised cache endpoint; membership-driven refresh/reconnect reconciliation is not implemented
- `/cache` and `/store-cache` support GET/HEAD; the planned `/cache-info` endpoint is absent

## HTTP API and client
- [internal/api/server.go](../internal/api/server.go): daemon-owned `Services`; `Server.Handler` wires middleware, health and domain routes
- HTTP has no authentication/authorization middleware or TLS listener; NATS mTLS does not protect the API, so remote access requires host-level protection
- API domain files stay in one package and register only their own routes:
  - [cluster.go](../internal/api/cluster.go): creation and identity
  - [hosts.go](../internal/api/hosts.go): host reads and local builds
  - [installers.go](../internal/api/installers.go): pinned installer registration, method catalogs, compatibility checks and gated launch/finalize submission
  - [repositories.go](../internal/api/repositories.go): repository reads, creation and refresh
  - [recipes.go](../internal/api/recipes.go): definitions, edits and sync
  - [configurations.go](../internal/api/configurations.go): versions, inputs, collection, upgrades and previews
  - [tasks.go](../internal/api/tasks.go): state, mutations, outputs and events
  - [logs.go](../internal/api/logs.go): retained logs and streams
  - [cache.go](../internal/api/cache.go): forwarding
  - [http.go](../internal/api/http.go): bounded JSON, errors, paging, versions and SSE framing
- cluster destroy/recover, repository update/remove, credential CRUD, host assignment/disks/metrics, enrollment, membership/power, deployment/install and publication routes are absent
- task lists implement cursor/limit only; CLI host/status/type filters are ignored
- task events deliver the current KV snapshot and changes, not historical replay from `Last-Event-ID`; log streams do replay by sequence
- host-log read routes exist, but the daemon has no host-log producer; build stderr is persisted under task logs
- [internal/client/client.go](../internal/client/client.go): shared bounded JSON client
  - `New`, `Close`, `request`, `Do`: traced HTTP lifetime and requests; never silently retry mutations
  - `Page`, `Error`, `UnknownSubmission`: paging, API failure and uncertain mutation results
  - `incomplete`, `responseError`, error formatting and `Unwrap`: failure classification
- [internal/client/stream.go](../internal/client/stream.go): `Client.Watch` reconnects with cursor/retry state; `readEvents` parses bounded SSE frames; callback/protocol errors stop delivery

## CLI and consoles
- [internal/cli/cli.go](../internal/cli/cli.go): `Execute` owns telemetry/status; `NewCommand` binds Cobra/Viper configuration; `daemonCommand` maps runtime flags
  - `call`, `watch`: shared-client output
  - `definition`, `required`, `flag`, `boolean`: bounded JSON/YAML input and flag helpers
- [internal/cli/read.go](../internal/cli/read.go): `get`, `addReads`, `child` assemble escaped reads, paging, filters and streams
- [internal/cli/mutate.go](../internal/cli/mutate.go): `addMutations` registers domain actions
- [internal/cli/versions.go](../internal/cli/versions.go): `versionFlag`, `versionPath`, `addVersionedCommands` map immutable lifecycle operations
- [internal/cli/hosts.go](../internal/cli/hosts.go): `addHostJobs` maps build/deploy/install/rollback/installer/media requests
- [internal/cli/installers.go](../internal/cli/installers.go): installer registration, pair selection and finalizer checks/submission
- [internal/cli/secrets.go](../internal/cli/secrets.go): `secretInput`, `addSecrets` use bounded stdin/hidden prompts, never password arguments
- [internal/cli/ui.go](../internal/cli/ui.go): `uiCommand` opens the shared-client terminal console; `ui` and `tui` are aliases
- [cmd/nixmox/main_js.go](../cmd/nixmox/main_js.go): WASM entrypoint registering the go-app browser console
- [internal/ui/views/connection.go](../internal/ui/views/connection.go): shared HTTP snapshots, mutations and selected task/log streams
- [internal/ui/components/build/model.go](../internal/ui/components/build/model.go): shared Nix activity/log projection for terminal/browser renderers
- `internal/ui` retains presentation snapshots, not authoritative cluster state or domain workflow ownership

## logging and telemetry
- [internal/util/logging.go](../internal/util/logging.go): context-correlated logging and bounded redaction
  - `base`, `From`, `With`: logger selection and correlation
  - `Configure`: local JSON/OTel fan-out plus local-only exporter error logging
  - `clean`, `Error`, `cleanAttr`: secret, error and nested-attribute sanitization
  - `safeHandler.Handle/WithAttrs/WithGroup`: preserve sanitization and span IDs
  - `contextHandler.Enabled/Handle/WithAttrs/WithGroup`: preserve correlation on derived handlers
- [internal/util/otel.go](../internal/util/otel.go): `Config` and `Start` own providers, W3C propagation, optional OTLP/HTTP exporters and shutdown flushing
- provider lifetime is separate from retained-log persistence in `cluster.Logs`; local/OTLP process logs are not automatically retained as host logs
- `util.Start` supplies no literal-secret redaction list; sensitive structured attributes are masked, but task output cannot be assumed to scrub arbitrary secrets

## planned enrollment and networking
- `internal/enrollment`: observer-tagged discovery hints, approval, CSR exchange, issuance and rejection records
- `internal/network`: endpoint publication and preferred connection candidates; local installer Wi-Fi scanning/configuration
- these packages are intended boundaries, not implemented services

## deferred runtime and task work
- daemon startup must restore pending/full members as NATS clients without starting JetStream servers
- persistent tasks must be saved locally, exempt from local unknown marking, and resumed from checkpoints when the daemon returns
- global cleanup must mark running tasks unknown after their owner has been offline for 30 minutes; this cleanup is also still missing
- these requirements are not implemented in this installer work; live transitions remain gated until recovery and safe membership demotion are wired

## installer
Location: [internal/installer](../internal/installer)
- `installers/`: general and QEMU/KVM profiles, immutable pinned build definitions and hardware compatibility checks
- `service.go`, `checks.go`, `inspect.go`: NATS `INSTALLERS` registration, local host observations and ranked installer/prepare pairs; explicit IDs do not bypass incompatibility
- `launch.go`, `build.go`: capture selected build inputs, link a build child task, recheck compatibility, call demotion/handoff boundaries and checkpoint before kexec; success requires a changed boot ID, installer environment and exact staged system path
- `prepare/`: method interface and kexec checks/execution; uses `kexec --load` followed by `systemctl kexec`, not an immediate forced reboot
- `finalize/`, `finalize.go`: general method checks and task runner; require a separate mounted root and preservation callback, then delegate closure/profile/bootloader setup to `nixos-install --system`
- built-in prepare/finalize method catalogs are code-owned; registered installer definitions and launch/finalize tasks are NATS-backed
- daemon registers both runners, but leaves the `Transition` boundary unwired: safe demotion, private boot payload staging, persistent-task registration and installed-state preservation still need implementation
- launch/finalize return unavailable before submitting work until that boundary is wired; compatibility results report execution readiness separately
- remote dispatch/transfer, fresh disk partitioning/formatting, enrollment and media writing remain unimplemented
- checks cover generic x86_64 and UEFI ARM64 platforms, not board-specific support or proof that a particular kernel will boot

## installer images
- [nix/installers](../nix/installers): shared installer environment, general hardware profile and QEMU/virtio VM profile
- exports `installer`, `installer-vm`, `installer-kexec` and `installer-vm-kexec` packages for x86_64-linux and aarch64-linux
- ISO images support UEFI; x86_64 also supports BIOS. Generic ARM64 images are not Raspberry Pi firmware images
- kexec outputs use the upstream NixOS kernel/self-contained initrd bundle; they do not implement Nixmox prepare or identity handoff
- local and serial consoles launch the Nixmox TUI automatically; profiles enable the installer daemon and key-only SSH, with no embedded credentials or disk layout
- networking belongs in the Nixmox TUI; its network controls/backend remain unimplemented, not replaced with an external setup UI
- customize through `nixosModules.installer` or `nixosModules.installer-vm`; add authorized keys or network settings in the consuming configuration
- run `nix develop -c just check-installers` to evaluate all variants and `nix develop -c just build-installer installer-vm` to build the native VM ISO
- ARM64 builds require an ARM64 builder or configured emulation; live prepare/finalize integration, enrollment and fresh installation remain unavailable

## tests and development
- [internal/testutil/nats.go](../internal/testutil/nats.go): reusable `JetStream` TLS fixture with cleanup; not a production multi-peer bootstrap test
- write usage examples and high-level integrations; reuse fixtures rather than new harnesses
- [poc/jetstream_bootstrap_test.go](../poc/jetstream_bootstrap_test.go): experimental topology coverage, separate from runtime wiring
- [internal/daemon/runtime_test.go](../internal/daemon/runtime_test.go): HTTP cluster/recipe/configuration lifecycle and cold-restart persistence
- [internal/configuration/lifecycle_test.go](../internal/configuration/lifecycle_test.go): real Git/JetStream import, sync, fork, independent realizations, upgrades and previews
- [internal/api/configurations_test.go](../internal/api/configurations_test.go): independent realizations over HTTP
- [installer examples](../internal/installer/checks_test.go), [return checkpoint test](../internal/installer/launch_test.go) and [installer CLI integration](../internal/api/installers_test.go): selection, finalizer checks, runner return validation and real CLI/API/NATS registration; not boot or task-manager restart recovery tests
- [identity tests](../internal/identity/identity_test.go): persistence, exclusive ownership and certificate enrollment
- [task tests](../internal/task/manager_test.go): durable review/approval and linked retries
- [source tests](../internal/source/source_test.go): pinned checkout and safe worktree access
- [registry tests](../internal/configuration/registry_test.go): discovery, preserved pins and explicit refresh
- [build examples](../internal/host/build_test.go): Nix result/store-path parsing; [build integration](../internal/host/build_integration_test.go): real build/log/monitor pipeline
- [cache tests](../internal/cache/cache_test.go): signed metadata, NAR, gateway and real Nix copying; the entire test skips without `NIXMOX_TEST_STORE_PATH`
- [credential tests](../internal/credentials/cipher_test.go): SSH-derived identities and SOPS recipient rotation
- [log tests](../internal/cluster/logs_test.go): replay-to-live delivery; [client tests](../internal/client/client_test.go): SSE reconnect and HTTP-to-NATS tracing
- [CLI tests](../internal/cli/cli_test.go): command usage, secret input and exact review binding
- [render examples](../internal/recipe/render_test.go), [field examples](../internal/recipe/fields_test.go), [output examples](../internal/recipe/output_test.go): typed templates/selections and template/raw/SOPS outputs
- [collector examples](../internal/configuration/collector_test.go): credential-ID and injected host callbacks plus unavailable-host behavior; other built-ins are covered by lifecycle/runtime tests
- [logging examples](../internal/util/logging_test.go), [telemetry examples](../internal/util/otel_test.go): correlation, redaction, shutdown and restored global state

Run through the devshell: `nix develop -c just <recipe>`. Registry, cache and real-build tests are opt-in and skip in the default suite.
- `test`: full Go suite
- `test-backend`: backend checks, excluding daemon, UI and PoC
- `test-backend-race`: backend plus daemon under the race detector
- `test-runtime`: cluster creation and cold restart
- `test-configurations`: recipe/configuration/API usage and lifecycle checks
- `test-installers`: installer examples and CLI/API/NATS wiring plus affected packages; no reboot, kexec or disk operations
- `test-ui`: shared UI examples and HTTP workflows
- `test-build-monitor`: real Nix build, retained logs, SSE and shared monitor
- `test-registry`: real Nix configuration discovery
- `test-cache`: signed-cache copying with a real store fixture
- `test-logging`, `test-otel`: logging/redaction and telemetry lifecycle examples
- `build`, `run`: native executable and CLI development
- `ui-assets`: WASM and matching Go runtime assets

## build and configuration
- [flake.nix](../flake.nix): x86_64/aarch64 Linux packages, devshell, formatter and browser assets; exports `nixosModules.default`
- [flake.lock](../flake.lock): pinned Nix inputs
- [.envrc](../.envrc): `use flake` through direnv
- [go.mod](../go.mod), [go.sum](../go.sum): Go toolchain/dependencies and checksums
- [nix/module.nix](../nix/module.nix): `services.nixmox`, typed configuration, boot environment, package override, private state and systemd service
- [justfile](../justfile): build, run and test recipes
- the NixOS module does not configure cache trust, firewall access or SSH host-key provisioning; its generated configuration is public in the Nix store

## validation
Checked against the working tree on 2026-11-03, including uncommitted files.
- reviewed non-UI source ownership, API/CLI mappings, daemon callers, test assertions and Nix/just configuration
- passed `nix develop -c just test`, including daemon cold restart and the bootstrap PoC
- passed `nix develop -c just test-registry test-cache test-build-monitor` with real Nix operations enabled
- these checks do not validate multi-host enrollment, deployment/install, physical hardware, a booted NixOS module or interactive browser/terminal behavior

## maintenance
- update the owning entry when files or responsibilities move
- group helpers by responsibility; keep domain lifecycle and transport boundaries explicit
- verify actual service wiring before changing status notes; keep planned features distinct from existing code
