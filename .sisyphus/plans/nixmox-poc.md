---
title: "nixmox POC — Proxmox-like management layer for NixOS"
date: 2026-08-06
status: draft
---

# nixmox POC — Proxmox-like Management Layer for NixOS

## Overview

nixmox is a Proxmox-like management layer for NixOS homelab hosts. NixOS itself is
already declarative (a git repo of flakes); nixmox covers the *imperative gap*: cluster
visibility, one-off host actions (shutdown/reboot), and GitOps-driven
`nixos-rebuild switch` with per-host auto/manual apply. It is a single Go binary that
embeds an HTTP server (chi, server-rendered Go templates + htmx, `embed.FS`) and an
embedded NATS server with JetStream for cluster state. The CLI (`cobra`) talks to the
running daemon over its localhost HTTP API. Every node runs the full stack; a single
node must function standalone (like a standalone Proxmox node). Validation is via
NixOS VM tests (`nixosTest` / QEMU multi-node) plus Go unit tests.

### Locked decisions (do not reopen)

| Area | Decision |
|---|---|
| Language / binary | Go single binary; `cobra` CLI: `nixmox serve`, `nixmox join <peer>`, `nixmox status`, `nixmox version` |
| RPC / state sync | Embedded NATS server per node (`github.com/nats-io/nats-server/v2/server`) with JetStream; KV buckets for cluster state |
| Standalone | 1 node runs the full stack and is fully functional |
| Apply | Local `nixos-rebuild switch --flake <local-clone>#<hostname>` per host |
| Portal | Server-rendered Go templates + htmx, embedded via `embed.FS`; `chi` router; no JS build step |
| Auth | NONE for POC — trusted LAN, plain HTTP, no join verification (documented accepted risk) |
| Validation | NixOS VM tests (`nixosTest` / QEMU multi-node) for milestone validation |
| Config | env vars + config file |
| Persistence | Default `/var/lib/nixmox`; holds NATS JetStream store, git clone, node identity, job data |
| Scheduler | `robfig/cron/v3` with `Recover()` + `DelayIfStillRunning()` |
| Passphrase / tokens | Config must leave room to add token auth later without redesign (POC constructs auth middleware as no-op) |
| Agent runs as | root (systemd service) — required for `nixos-rebuild` |
| Cluster bootstrap UX | Fresh node boots **unclustered**; portal setup screen offers "Create new cluster" (enter name) or "Join existing cluster" (enter `host:port`); no CLI-only flow required |
| Apply record | After every apply/rollback attempt the agent persists `{timestamp, status, gitSHA, genBefore, genAfter}` to the `hosts.<nodeID>` KV record (latest) and `applylog.<nodeID>.<unix-ts>` keys (history, pruned to last 50) |
| Flake outputs | `nixosConfigurations.node1/node2/node3` — 3 demo/test VMs importing the NixOS module, shared by `nixosTest` checks and the interactive demo |
| Two gitops modes | (a) **automated nixosTest**: hermetic bare-repo fixture created by the test driver; (b) **interactive demo / real dogfooding**: real git remote URL configured through the portal |
| Interactive demo | `nix run .#demo` boots 3 QEMU VMs interactively for the user to click through the portals |
| NATS supervisor | A named component owns the embedded-server lifecycle and supports **runtime reconfigure** (rewrite cluster name + routes, restart embedded server in-process, no daemon restart) — this is how portal-driven join is possible |

---

## Design constraints & risks

### 1. The 2-node JetStream quorum trap + R-scaling strategy

KV stream replicas (`R`) determine how many copies of a key live across JetStream
peers. **R=2 is strictly forbidden**: with 2 nodes total and R=2, the KV becomes
read-only the instant either node dies (no quorum), which is worse than R=1.

**Strategy (locked):**

1. Create all KV buckets with **R=1** at bootstrap. R=1 is durable enough on a single
   node and tolerates 1-of-2 node loss in a 2-node cluster.
2. Maintain a **replica auto-scaling task** (M5/M7): when the cluster reaches **≥3
   JetStream nodes**, run `js.UpdateStream` on each KV bucket to **R=3** (supported
   since `nats-server` 2.9, no downtime).
3. Pin `github.com/nats-io/nats-server/v2` **≥ v2.10.19** (stale-read / direct-get
   fixes).
4. **Never** auto-scale from R=1 → R=2 → R=3; jump straight 1→3 only when ≥3 peers.

**AllowDirect pitfall:** KV buckets default `AllowDirect=true`, which lets followers
serve *stale* direct gets under load. If CAS/consistency errors appear on the `config`
bucket (remote-apply decisions must be coherent), recreate it with `--no-allow-direct`
(or the `nats.CreateKVBucket` equivalent `AllowDirect: false`). Document in code.

### 2. Self-restart guards for `nixos-rebuild` from a daemon

If the agent's own systemd unit changes during `nixos-rebuild switch`, systemd kills the
agent **mid-apply**, leaving the system in a broken half-switched state. The shipped
NixOS module MUST set (mirrors NixOS's own `services.self-deploy` pattern):

```nix
systemd.services.nixmox.restartIfChanged = false;
systemd.services.nixmox.unitConfig.X-StopOnRemoval = false;
systemd.services.nixmox.serviceConfig.KillMode = "process";
```

Additionally the agent process must **trap SIGTERM** and (a) stop scheduling new jobs,
(b) let any in-flight `nixos-rebuild` finish (or leave it running and detach), (c) shut
down NATS, (d) shut down the HTTP server — **in that order**. See "Graceful shutdown
ordering" below.

### 3. Apply record (rich schema, v1 source of truth)

`/run/current-system` only embeds the **nixpkgs** SHA, **not** the config repo SHA. v1
source of truth is the agent's own **ApplyRecord**, persisted after every apply/rollback
attempt (regardless of success):

```json
{
  "timestamp": "2026-08-06T12:34:56Z",
  "status": "success",           // "success" | "failed"
  "gitSHA": "abc1234",           // repo SHA applied (see source-resolution below)
  "genBefore": 42,               // NixOS generation number BEFORE switch
  "genAfter": 43                // NixOS generation number AFTER switch (== genBefore on failure)
}
```

- **genBefore / genAfter** are NixOS generation numbers captured by the agent before
  and after `nixos-rebuild switch`. Parse the active generation by reading the symlink
  target of `/nix/var/nix/profiles/system-N-link` for the highest `N`, or by parsing
  `nixos-rebuild list-generations --json` output. `genBefore` doubles as the precise
  target for the manual **rollback** button (`nixos-rebuild switch --rollback` rolls to
  `current - 1`, i.e. `genBefore`; this is intentional and matches user expectation).
- **gitSHA source resolution** (precedence, evaluated at record-write time):
  1. `/etc/nixmox-flake-rev` if present and non-`"unknown"` (the user flake writes
     `environment.etc."nixmox-flake-rev".text = self.rev or self.dirtyRev or "unknown";`)
  2. else `mgr.CurrentSHA()` (the agent's local clone HEAD after the switch ran).
  This precedence is locked and replayed consistently on every record write.
- **Storage strategy (sealed across M4/M5):**

  | Layer | Where | Source of truth | When written |
  |---|---|---|---|
  | M4 (pre-cluster) | `{data_dir}/applied.json` (latest only) + in-memory applylog | local | after every apply/rollback attempt |
  | M5 (cluster) | `hosts.<nodeID>` KV: latest ApplyRecord embedded in the host record | cluster | M5's heartbeat writes mirror the local latest |
  | M5 (cluster) | `applylog.<nodeID>.<unix-ts>` KV keys: history/audit trail (TTL = none on success; TTL = 7d on failure to allow stale-leave cleanup) | cluster | appended per attempt |

  The `config` KV bucket is R-durable; the `applylog.<nodeID>.<ts>` keys live in the
  same `hosts` KV bucket keyed by prefix so they share replication + TTL semantics
  (history rows use a longer TTL — 30d — distinct from the 30s heartbeat TTL on
  `hosts.<nodeID>`).

- **Prune policy:** keep the **last N=50** applylog keys per host. The agent runs a
  cron-style prune during heartbeat (if `len(applylog.<nodeID>.*) > 50`, delete the
  oldest by timestamp). This bounds KV growth without portal intervention.
- **Optional hardening (document, not required in POC):** the same
  `/etc/nixmox-flake-rev` mechanism above already covers repo-SHA ground truth when
  the user opts in.

Portal display (cross-M4/M5 task): the host **row** shows pulled SHA vs last
ApplyRecord (status, gitSHA, gens, timestamp); the host **detail view**
(`/hosts/<nodeID>`) shows the full applylog history (last 50, newest first).

### 4. No-auth accepted risk (POC)

- Plain HTTP on trusted LAN; no join verification; any node that can reach the
  cluster URL can join; any LAN host can talk to the HTTP API.
- The HTTP stack MUST construct an `auth.Middleware` interface with a **no-op
  implementation** wired by default, so adding a token/header check later is a
  one-provider change, not a redesign.
- NATS must be configured to **listen only on the cluster/LAN interface** (config
  `Host`), never `0.0.0.0` unless explicitly configured.
- Document this as an accepted risk in `DESIGN.md` and the NixOS module options.

### 5. v1 out of scope (explicit exclusions / guardrails)

- RBAC / multi-user auth
- Webhooks / external notifications
- Automatic health-check rollback after a failed `switch`
- Drift detection (continuous `nixos-rebuild dry-activate` diffing)
- Multi-arch builds (x86_64-linux only for POC checks)
- Metrics / monitoring / Prometheus endpoint
- Private-repo UI in the portal (private-repo auth via SSH key path or netrc in config
  file only)
- Hot config reload (restart the daemon to pick up config changes — documented)
- Auto R=1→R=3 promotion is the only replica autoscaling; no manual rebalance UI in v1
- **NATS supervisor runtime reconfigure** is **in scope** (portal-driven create/join
  needs it) — what's out of scope is changing the cluster *name* on a running cluster
  (cluster identity is fixed at create time).
- Per-host deploy windows / maintenance windows
- Concurrent apply on the same host: **forbidden by design** — only one apply/rollback
  job per host at a time (enforced by the job runner's per-host lock; cron job uses
  `DelayIfStillRunning()` so a long-running apply blocks the next poll safely)

---

## Repo layout (target tree)

```
nixmox/
├── go.mod
├── go.sum
├── Makefile                       # build runner (justfile considered; Makefile picked for lower friction)
├── README.md                      # (exists)
├── LICENSE                        # (exists)
├── .gitignore                     # (exists)
├── DESIGN.md                      # architecture + accepted risks (M7)
├── AGENTS.md                      # agent/redesign guidance (M7)
├── docs/adr/                      # ADRs (M7)
│   ├── 0001-no-auth-poc.md
│   ├── 0002-embedded-nats.md
│   └── 0003-jobs-as-files.md
├── cmd/
│   └── nixmox/
│       └── main.go                # cobra root: serve / join / status / version
├── internal/
│   ├── config/
│   │   ├── config.go              # env + file loader, defaults
│   │   └── config_test.go
│   ├── server/
│   │   ├── server.go              # chi router, /health, /api/v1/*, lifecycle
│   │   ├── auth.go                # no-op auth.Middleware (token pluggable later)
│   │   ├── handlers.go            # JSON API handlers
│   │   └── server_test.go         # httptest
│   ├── portal/
│   │   ├── templates/             # *.html (embed.FS)
│   │   ├── assets/                # htmx.min.js, minimal css (vendored, embed.FS)
│   │   ├── portal.go              # template render, routes
│   │   └── portal_test.go
│   ├── jobs/
│   │   ├── store.go               # per-job JSON file in {data_dir}/jobs/
│   │   ├── machine.go             # state machine: pending→building→applying→applied|failed
│   │   ├── runner.go              # goroutine runner, streaming output, per-host lock
│   │   ├── cron.go                # robfig/cron v3 wrapper
│   │   ├── machine_test.go        # table-driven transition matrix
│   │   └── runner_test.go
│   ├── gitops/
│   │   ├── client.go              # git client interface (mockable)
│   │   ├── poller.go              # cron fetch, SHA compare, pull/reset --hard
│   │   ├── clone.go               # clone management in {data_dir}/git/repo
│   │   ├── poller_test.go         # mock git client
│   │   └── client_test.go
│   ├── apply/
│   │   ├── apply.go              # nixos-rebuild switch / dry-activate / rollback
│   │   ├── record.go             # applied-SHA persisted record + /etc/nixmox-flake-rev fallback
│   │   ├── apply_test.go
│   │   └── record_test.go
│   ├── cluster/
│   │   ├── node.go               # node UUID, identity (node.json), ApplyRecord type def
│   │   ├── supervisor.go         # NATS supervisor: owns embedded lifecycle, RUNTIME RECONFIGURE (M5)
│   │   ├── join.go               # internal join API (shared by CLI + portal), POST /api/v1/cluster/join
│   │   ├── heartbeat.go          # periodic KV writes, TTL ~30s, mirrors latest ApplyRecord
│   │   ├── applylog.go           # applylog.<nodeID>.<ts> history append + prune (N=50)
│   │   ├── scalereplicas.go      # R=1 → R=3 at ≥3 nodes
│   │   ├── remove.go             # tombstone + rebalance (M7)
│   │   ├── recover.go            # agent restart/rejoin recovery (M7)
│   │   └── *_test.go
│   └── natsx/
│       ├── embed.go              # embedded NATS server lifecycle (used by supervisor)
│       ├── kv.go                 # KV bucket helpers, meta-leader wait, R-scaling
│       └── embed_test.go
└── nix/
    ├── flake.nix                  # dev shell, package, NixOS module, VM tests, demo, nixosConfigurations
    ├── module.nix                 # NixOS module (self-restart guards, /var/lib/nixmox)
    ├── package.nix                # build go binary
    ├── devshell.nix
    ├── configurations.nix        # nixosConfigurations.node1/node2/node3 — demo/test VMs (shared by checks + demo)
    ├── vm-common.nix              # common QEMU port-forwarding + eth1/static-IP module used by demo (M8)
    ├── demo.nix                   # apps.x86_64-linux.demo launcher (writeShellApplication, M8)
    └── checks/
        ├── single-node-portal.nix
        ├── jobs-runner.nix
        ├── gitops-pull.nix
        ├── apply-switch.nix
        ├── cluster-join.nix
        ├── remote-actions.nix
        └── full-suite.nix
```

```
# Portal templates (Amendments 1 + 5):
internal/portal/templates/
├── base.html
├── portal.html                     # cluster view (multi-host) — M0..M5 progressively
├── setup.html                      # unclustered bootstrap screen (M1 stub, M5 functional)
├── host-detail.html                # per-host detail with applylog history — M4 (local) / M5 (cluster)
├── jobs.html
└── gitops.html
```

### Persistence dir layout

Default `/var/lib/nixmox` (configurable via `NIXMOX_DATA_DIR` env or config file):

```
/var/lib/nixmox/
├── nats/             # JetStream store dir (server.Options.JetStream.StoreDir)
├── git/
│   └── repo/         # local clone of the GitOps flake repo
├── node.json         # { uuid, hostname, agent_version, created_at, cluster_name?, routes? }
├── applied.json      # latest ApplyRecord (M4, pre-cluster); superseded by KV mirror in M5
├── jobs/             # ONE FILE PER JOB (*.json), named by job UUID
└── config.json       # generated/normalized config snapshot (optional)
```

**Choice: jobs as a directory of JSON files (`jobs/`), not a single `jobs.db`.**
Justification: (a) inspectability — `cat jobs/<uuid>.json` to debug; (b) crash safety —
atomic file rename per transition, no shared DB to corrupt; (c) no extra dependency
(no sqlite/CGO); (d) POC scale is tiny (one host, a handful of jobs/day). The tradeoff
is no indexed queries, but the portal/job-list API walks the directory and sorts in
memory — fine at POC scale. If this becomes hot, swap the store interface for sqlite
without touching callers (the `jobs.Store` interface gates this).

**Ownership/permissions:** `/var/lib/nixmox` and children are owned by `root:root`,
mode `0750` (parent `0750`, `nats/` `0700` since it holds KV data). Enforced by the
NixOS module's `systemd.services.nixmox` + a `StateDirectory=` directive.

---

## TODOs

> Each milestone is a parallel wave of atomic, TDD-oriented tasks.
> Test files are written **with** implementation (table-driven unit tests; `httptest`
> for HTTP handlers; mock git client for poller; `nixos-rebuild dry-activate` inside
> `nixosTest` for end-to-end).
> Every task ends with the exact verification command(s) and done-criteria.
> Waves map to milestones: M0 → Wave 0, M1 → Wave 1, ….

---

## M0 — Skeleton (Wave 0)

**Goal:** compilable Go project with cobra CLI, config loader, flake.nix dev shell +
package + NixOS module stub, lint/CI, Makefile. `nixmox version` prints something
useful; `nixmox serve` starts an empty HTTP server on `127.0.0.1:8080` and exits on
SIGTERM.

- [ ] **0.1 Go module + cobra skeleton**

  **What to do:**
  - `go mod init github.com/helios/nixmox` (adjust module path if different — confirm in
    step 0.0 by reading `README.md`/`LICENSE`).
  - Add deps: `github.com/spf13/cobra`, `github.com/go-chi/chi/v5`,
    `github.com/robfig/cron/v3`, `github.com/nats-io/nats-server/v2`,
    `github.com/nats-io/nats.go`. Use the latest tagged versions; pin
    `nats-server/v2` **≥ v2.10.19**.
  - `cmd/nixmox/main.go`: cobra root command with `serve`, `join <peer>`, `status`,
    `version` subcommands. `version` prints `version: dev, commit: none, build: <date>`
    (wired via `-ldflags` later; for POC kept simple). `join` and `status` are stubs
    that print "not implemented yet" (implemented in M5/M1 respectively).
  - `serve` loads config (task 0.3), constructs an empty chi server, listens on
    `127.0.0.1:8080`, traps SIGINT/SIGTERM and shuts down cleanly.

  **Must NOT do:**
  - No business logic in M0 — only scaffolding + lifecycle wiring.
  - No NATS, no portal, no jobs.

  **References:**
  -cobra root+subcommand idiom: see cobra README
  Examples section (https://github.com/spf13/cobra#usage).
  -chi graceful shutdown: https://github.com/go-chi/chi#graceful-shutdown
  -nats-server embed entrypoint (for later tasks): see CHANGELOG note that this plan
  pins ≥ v2.10.19 (Mozilla Public License 2.0 — add a `NOTICE` or check LICENSE
  compatibility; Go is BSD-3-Clause, GPL-compat is fine).

  **Acceptance Criteria:**
  - [ ] `go build ./...` exits 0.
  - [ ] `./nixmox version` prints a non-empty version line.
  - [ ] `./nixmox serve` starts, responds `200 OK` on `GET /health` (added in 0.4),
        and exits 0 within 1s of SIGTERM.

- [ ] **0.2 Config loader (`internal/config`)**

  **What to do:**
  - `internal/config/config.go`: struct `Config` with fields — `DataDir` (default
    `/var/lib/nixmox`), `HTTPAddr` (default `127.0.0.1:8080`), `NATSHost`,
    `NATSPort` (default `6222`), `NATS ClusterName`, `Routes []string` (peer URLs),
    `GitRepoURL`, `GitBranch` (default `main`), `GitPollInterval`
    (default `5m`), `FlakePath` (default `.#<hostname>`), `SSHKeyPath`, `NetrcPath`,
    `HostnameOverride`, `Version`, `Commit`.
  - Loader precedence: defaults < file (`/etc/nixmox/config.yaml` or
    `--config <path>`) < env (`NIXMOX_*`).
  - Use `gopkg.in/yaml.v3` for the file (or `encoding/json`; pick yaml for human
    readability). Validate required fields at `serve` time, not at parse time (so
    `join`/`status` work without a full config).
  - `--config` flag and `NIXMOX_CONFIG` env override the path.

  **Must NOT do:** no hot reload; document that the daemon must be restarted to pick up
  config changes.

  **References:** envconfig / viper patterns — keep deps minimal: just `yaml.v3`.

  **Acceptance Criteria (TDD):**
  - [ ] `internal/config/config_test.go`: table-driven tests covering defaults, file
    override, env override, precedence (env > file > default).
  - [ ] `go test ./internal/config/...` → all PASS.

- [ ] **0.3 chi HTTP server + `/health` (`internal/server`)**

  **What to do:**
  - `internal/server/server.go`: `Server` struct holding `chi.Mux`, `*http.Server`,
    `Config`. `New(cfg)` constructor; `Start(ctx)` blocks until ctx cancelled; runs
    `http.Server.Shutdown` on ctx done. Mount `GET /health` → `{"status":"ok","version":<v>}`.
  - `internal/server/auth.go`: `type Middleware interface { Wrap(http.Handler)
    http.Handler }` + `NoopAuth{}` implementation (no header checks). Wire as
    `r.Use(authMiddleware.Wrap)`. This is the pluggable auth seam.
  - SIGTERM handling lives in `cmd/nixmox/serve.go`, not in `server.go` (server takes a
    context; the command owns `signal.NotifyContext`).

  **Must NOT do:** no portal routes, no API v1 handlers beyond `/health`.

  **Acceptance Criteria (TDD):**
  - [ ] `internal/server/server_test.go` using `httptest.NewRecorder`: `GET /health`
    returns `200` and `{"status":"ok"}`.
  - [ ] `NoopAuth.Wrap` is a passthrough (table test: request passes through
    unchanged).
  - [ ] `go test ./internal/server/...` → PASS.

- [ ] **0.4 Makefile + lint + CI**

  **What to do:**
  - `Makefile` targets: `build`, `test` (`go test ./...`), `lint` (`golangci-lint
    run`), `fmt` (`gofmt -s -w .`), `tidy` (`go mod tidy`), `clean`, `run` (build +
    `./nixmox serve`), `install` (copies to `$(PREFIX)/bin`).
  - `.golangci.yml`: enable `errcheck`, `govet`, `staticcheck`, `unused`, `gosimple`,
    `gocyclo` (`max-complexity: 20`), `gofmt`. Disable `lll` (template lines).
  - `.github/workflows/ci.yml`: matrix on push/PR — `go test ./... -race`, `golangci-lint
    run`, `nix build .#checks.x86_64-linux.single-node-portal` (added in M1; commented
    out in CI until M1 exists so M0 CI is green).
  - `goimports` enforced via `golangci-lint`.

  **Acceptance Criteria:**
  - [ ] `make build && make test && make lint` all exit 0 locally.
  - [ ] CI workflow file exists and `go test -race ./...` step passes on the runner.

- [ ] **0.5 flake.nix (dev shell + package + NixOS module stub)**

  **What to do:**
  - `nix/flake.nix` exposed at repo root via a thin `flake.nix` that imports `nix/` —
    or keep a single top-level `flake.nix` that defines everything (pick the simpler:
    single top-level `flake.nix`).
  - `nix/devshell.nix`: `mkShell` with go, golangci-lint, gopls.
  - `nix/package.nix`: `buildGoModule { src = ./.; vendorHash = lib.fakeHash; }` (set
    real `vendorHash` after first `nix build`; pin via `go mod vendor` workflow).
  - `nix/module.nix`: stub `services.nixmox.enable` + `services.nixmox.package` +
    `systemd.services.nixmox` with the **self-restart guards already wired** (so they
    exist from day 1):
    ```nix
    systemd.services.nixmox = {
      restartIfChanged = false;
      unitConfig.X-StopOnRemoval = false;
      serviceConfig.KillMode = "process";
    };
    ```
  - flake outputs: `packages.x86_64-linux.default` (the binary), `devShells.x86_64-linux.default`, `nixosModules.default` (= `nix/module.nix`), `checks.x86_64-linux.*` (empty for M0; added per-milestone).
  - **`nixosConfigurations.node1/node2/node3` (Amendment 2, M0):** three demo/test VM
    configurations, each importing the nixmox NixOS module with
    `services.nixmox.enable = true`. Define in `nix/configurations.nix`, wired as flake
    outputs `nixosConfigurations.<name> = lib.nixosSystem { modules = [ self.nixosModules.default (mkNodeConfig name) ]; }`.
    These three VMs are the canonical way a developer runs the whole system locally:
    they are shared by (a) `nixosTest` checks in M1–M7 (referencing
    `nixosConfigurations.nodeN.config.system.build.toplevel` to compose VM test nodes)
    and (b) the interactive demo launcher in M8 (`apps.x86_64-linux.demo`, which drives
    `nixosConfigurations.nodeN.config.system.build.vm`). Each VM must be **headless**
    (`services.xserver.enable = false`), boot to multi-user, and use predictable
    hostnames (`node1`/`node2`/`node3`) — the test driver and demo launcher both depend
    on these names.
    - In M0 the three configs only need to **build** and reach `multi-user.target` in
      QEMU; no nixmox-specific assertions yet (those land with the per-milestone
      `checks.*`).

  **Must NOT do:** no VM tests in M0 (empty `checks` map is fine; M1 onwards adds them).

  **Acceptance Criteria:**
  - [ ] `nix develop` opens a shell with `go` on PATH.
  - [ ] `nix build .#default` builds the binary (after setting the real `vendorHash`).
  - [ ] `nix build .#checks.x86_64-linux.single-node-portal` is **absent** (will fail
        with "not found" — acceptable for M0; M1 adds it).

### M0 Done-Criteria

```bash
go build ./...                 # exit 0
go test ./... -race            # all PASS
make lint                      # exit 0
./nixmox version              # prints "version: dev ..."
./nixmox serve & PID=$!       # starts, listens on 127.0.0.1:8080
sleep 0.3
curl -sf http://127.0.0.1:8080/health     # {"status":"ok","version":"dev"}
kill -TERM $PID   # exits 0 within 1s
nix develop -c echo "shell ok"          # "shell ok"
nix build .#default                     # builds the go binary
nix build .#nixosConfigurations.node1.config.system.build.toplevel  # the 3 demo VMs build
nix build .#nixosConfigurations.node2.config.system.build.toplevel
nix build .#nixosConfigurations.node3.config.system.build.toplevel
```

**Commit strategy (M0):** atomic commits per task, final squashed to a single
`chore(skeleton): M0 go module, cobra, config, chi server, flake, CI`:

1. `chore(mod): init go module, cobra skeleton, deps`
2. `feat(config): env+file loader with precedence tests`
3. `feat(server): chi /health with pluggable noop auth`
4. `chore(build): Makefile, golangci-lint, GitHub Actions CI`
5. `feat(nix): flake devshell + package + NixOS module stub with self-restart guards`
6. `feat(nix): nixosConfigurations.node1/node2/node3 demo/test VMs (shared by checks + demo)`

---

## M1 — Single-node portal (Wave 1)

**Goal:** `nixmox serve` shows a portal page (server-rendered Go template + htmx,
embedded via `embed.FS`) listing this host: hostname, NixOS version (from
`/run/current-system` short SHA / `/etc/os-release`), uptime (`/proc/uptime` or
`getutxent`-style syscall), and agent version. `/api/v1/hosts` returns the same as
JSON. No clustering, no jobs, no GitOps yet — M1 proves the HTTP + template + embed
pipeline end-to-end.

**Expected files/packages created/touched:**
- `internal/portal/portal.go`, `templates/{base,portal,setup}.html`, `assets/{htmx.min.js,style.css}` (vendored)
- `internal/portal/portal_test.go`
- `internal/server/handlers.go` (adds `/api/v1/hosts` + `/` portal routes via `portal.Portal`; `/api/v1/bootstrap` read-only cluster-state probe used by the setup screen)
- `nix/checks/single-node-portal.nix` — NixOS VM test
- Repo root `flake.nix` extended to wire the check

- [ ] **1.1 Portal template engine + embed.FS**

  **What to do:**
  - `internal/portal/templates/base.html` (head, htmx script, `{block content}`),
    `portal.html` (table of hosts with columns: hostname, nixos version, uptime, agent
    version, status badge).
  - `internal/portal/portal.go`: `type Portal struct { tmpl *template.Template; fs
    fs.FS }`; `New()` parses `templates/*.html` via `template.New().ParseFS(templatesFS,
    ...)`. Use `//go:embed templates/* assets/*` to expose static assets at `/assets/`.
  - `Portal.Index(w, hosts)` renders `portal.html` with `[]Host`.
  - Add a `/partials/hosts` endpoint that returns just the hosts `<tbody>` fragment for
    htmx OOB swaps (set up for M5 heartbeat-driven polling, but M1 only needs the
    skeleton).
  - DECISION (orchestrator, 2026-08-06): the unclustered setup screen (`setup.html`) and
    `GET /api/v1/bootstrap` are DEFERRED to M5 (task 5.5 introduces both, fully
    functional). M1's `/` always renders `portal.html` with the 1-row hosts table — a
    placeholder-only M1 portal would show no host info, which defeats M1's purpose.

  **Must NOT do:** no JS build; no template inheritance beyond base/content; no fetch
  of remote hosts (M1 always shows the local host list of length 1).

  **Acceptance Criteria (TDD):**
  - [ ] `internal/portal/portal_test.go`: render `Portal.Index` with a fake `[]Host`
    and assert the output contains the hostname, "nixos", the version, and the status
    badge text.
  - [ ] `go test ./internal/portal/...` → PASS.

- [ ] **1.2 Host info provider**

  **What to do:**
  - `internal/cluster/node.go` (first taste — just the host info, no clustering yet):
    `type Host struct { NodeID string; Hostname string; NixOSVersion string; Uptime
    time.Duration; AgentVersion string; Status string }`.
  - `LocalHost(cfg)` reads: hostname (`os.Hostname()` or `cfg.HostnameOverride`),
    NixOS version (parse `/etc/os-release` `VERSION` line, fallback to
    `/run/current-system` symlink basename), uptime (`/proc/uptime` first field — cross-
    platform-friendly enough for NixOS), agent version from `cfg.Version`.
  - Status for M1 is always `"online"` (the local node).

  **Acceptance Criteria (TDD):**
  - [ ] `internal/cluster/node_test.go`: feed a fixture `/etc/os-release` text +
    `/proc/uptime` text via small reader interface; assert parsed fields.
  - [ ] `go test ./internal/cluster/...` → PASS.

- [ ] **1.3 Wire portal + `/api/v1/hosts` + `/api/v1/bootstrap` into the server**

  **What to do:**
  - `internal/server/handlers.go`: `GET /` → `portal.Index` (M1: always renders
    `portal.html` with the 1-row hosts table); `GET /api/v1/hosts` → JSON
    `[Host]` (M1: always `[LocalHost]`). `GET /partials/hosts` → hosts fragment.
  - `GET /api/v1/bootstrap` and the setup screen are NOT in M1 — introduced in M5.5.
  - Inside `Server.New`, mount `portal` routes and the JSON API routes under `/api/v1`.
  - All routes still pass through the no-op `auth.Middleware`.

  **Acceptance Criteria (TDD):**
  - [ ] `internal/server/server_test.go` (extend): `GET /` returns HTML containing the
    local hostname and the hosts table; `GET /api/v1/hosts` returns JSON `[{...}]`
    with `status == "online"`.
  - [ ] `make lint && go test ./internal/server/...` → PASS.

- [ ] **1.4 NixOS VM test `single-node-portal`**

  **What to do:**
  - `nix/checks/single-node-portal.nix`: `nixosTest` with one node running the nixmox
    NixOS module, `nixmox.enable = true`. Inside the test:
    ```nix
    machine.wait_for_unit("nixmox.service");
    machine.wait_for_open_port(8080);
    machine.succeed("curl -sf http://127.0.0.1:8080/health | grep -q ok");
    machine.succeed("curl -sf http://127.0.0.1:8080/api/v1/hosts | grep -q machine");
    machine.succeed("curl -sf http://127.0.0.1:8080/ | grep -q nixmox");
    ```
  - Expose as `checks.x86_64-linux.single-node-portal` in flake outputs.

  **Acceptance Criteria:**
  - [ ] `nix build .#checks.x86_64-linux.single-node-portal` exits 0 (QEMU boots +
    assertions pass).

### M1 Done-Criteria

```bash
go test ./... -race
./nixmox serve & PID=$!
sleep 0.5
curl -sf http://127.0.0.1:8080/                   # portal renders hosts table containing the local hostname
curl -sf http://127.0.0.1:8080/api/v1/hosts       # JSON array, length 1, status "online"
curl -sf http://127.0.0.1:8080/assets/htmx.min.js # htmx served (non-empty)
kill -TERM $PID   # exits 0 within 1s
nix build .#checks.x86_64-linux.single-node-portal   # PASS
```

**Atomic commits:**
1. `feat(portal): embed.FS templates + htmx fragment partial + hosts table`
2. `feat(cluster): LocalHost info provider (os-release, uptime)`
3. `feat(server): wire / and /api/v1/hosts + /api/v1/bootstrap`
4. `test(nix): single-node-portal nixosTest`

---

## M2 — Persistence + job runner (Wave 2)

**Goal:** durable job store (one JSON file per job in `{data_dir}/jobs/`), a
state-machine (`pending → building → applying → applied | failed`), a goroutine
runner that streams command output, `/api/v1/jobs` and `/api/v1/jobs/{id}` endpoints,
and a portal job list. Prove the end-to-end with a harmless **`nixos-rebuild
dry-build`** job (or `echo` for the unit tests). No clustering yet.

**Expected files/packages:**
- `internal/jobs/{store,machine,runner,cron}.go` + tests
- `internal/server/handlers.go` (extend with jobs endpoints)
- `internal/portal/templates/jobs.html` + `portal.Jobs`
- `nix/checks/jobs-runner.nix`

- [ ] **2.1 Job store (one JSON file per job)**

  **What to do:**
  - `internal/jobs/store.go`: `type Job struct { ID string; Kind string; Host string;
    Args []string; State string; CreatedAt, UpdatedAt time.Time; LogPath string }`;
    `type Store interface { Create(Job) error; Get(id string) (Job, error); List()
    ([]Job, error); Update(Job) error; AppendLog(id string, line string) error }`.
  - File implementation: `{data_dir}/jobs/<id>.json` with atomic rename (`tmp` +
    `os.Rename`). Log streamed to `{data_dir}/jobs/<id>.log` (append per line). `List()`
    reads the directory and sorts by `CreatedAt` descending.
  - Create `{data_dir}/jobs/` with `0700` on `New`.

  **References:** `os.WriteFile` + `os.Rename` atomic-write idiom.

  **Acceptance Criteria (TDD):**
  - [ ] `internal/jobs/store_test.go` using `t.TempDir()` as the data dir: tables
    covering Create→Get→List, Update transition, log append (read back bytes match), and
    concurrent Update (run 50 goroutines appending different log lines — final log
    contains all 50 lines, no interleaving corruption; use a mutex in the Store if
    needed).
  - [ ] `go test ./internal/jobs/...` → PASS.

- [ ] **2.2 State machine (table-driven)**

  **What to do:**
  - `internal/jobs/machine.go`: `func Next(current, event string) (string, error)` plus
    `IsValidTransition` against the matrix:
    - `pending --start--> building`
    - `building --succeeded--> applying` (or `applied` directly for non-apply kinds)
    - `applying --succeeded--> applied`
    - any of `building`/`applying` `--failed--> failed`
    - `applied`/`failed` are terminal; further events return `ErrInvalidTransition`.
    - `applied --rollback--> building` (used in M4; soft-add the transition now so M4
      has the seam).
  - Any unknown event/stage → `ErrInvalidTransition`.
  - **Runner hook seam (Amendment 1, design bridge to M4):** the runner exposes a
    `OnTerminal func(job Job)` callback invoked when a job enters `applied` or `failed`.
    M4 wires this callback to emit the `ApplyRecord` (with `genBefore`/`genAfter`
    captured by the M4 applier around the `nixos-rebuild` exec). M2 ships the noop
    default; M2 tests verify the callback fires on each terminal state. This keeps M2
    unaware of apply semantics while leaving the seam the M4 applier needs.

  **Acceptance Criteria (TDD):**
  - [ ] `internal/jobs/machine_test.go`: table-driven test over **every** valid
    transition plus every invalid one discovered by fuzzing (use a small explicit
    invalid-set, plus `testing/quick` to assert unknown events always error).
  - [ ] `go test ./internal/jobs/...` → PASS.

- [ ] **2.3 Goroutine runner with streaming output + per-host lock**

  **What to do:**
  - `internal/jobs/runner.go`: `type Runner struct { store Store; mu
    sync.Map[string]*sync.Mutex }` (per-host lock keyed by `job.Host`). `Start(ctx,
    job)` acquires the host lock (returns `ErrHostBusy` if held), transitions to
    `building`, runs `exec.CommandContext(ctx, name, args...)` with
    `cmd.Stdout/Stderr` plumbed line-by-line through `store.AppendLog`, transitions to
    `applied` (exit 0) or `failed` (non-zero / ctx.Err). Must NOT block the HTTP
    handler — returns the job ID immediately.
  - Job kinds: `"dry-build"`, `"echo"` (test only), `"apply"` (M4 stub), `"rollback"`
    (M4 stub).
  - **Graceful shutdown:** `Runner.Stop(ctx)` waits for in-flight jobs to finish (or
    `ctx` cancel propagates via `exec.CommandContext` and the goroutine records
    `failed`).
  - Cron-issued jobs (M3 apply poll) MUST be dispatched through the same `Runner.Start`
    so the per-host lock + `DelayIfStillRunning()` semantics compose.

  **Must NOT do:** no automatic retry; no parallel apply on same host; no health-check
  rollback (all excluded per v1).

  **Acceptance Criteria (TDD):**
  - [ ] `internal/jobs/runner_test.go`: (a) `echo` job → ends `applied`, log contains
    `hello`; (b) `exit 1` job → ends `failed`, log contains stderr (because `cmd.Run()`
    sets exit code); (c) two `Start` calls for same host → second returns `ErrHostBusy`,
    first runs to completion; (d) ctx cancel mid-run → job ends `failed` with a
    `context canceled` log line; (e) `OnTerminal` fires once per terminal state with the
    correct `Job.State` value (table-driven over both `applied` and `failed`).
  - [ ] `go test ./internal/jobs/... -race` → PASS.

- [ ] **2.4 `/api/v1/jobs` + portal Jobs view**

  **What to do:**
  - `internal/server/handlers.go`: `GET /api/v1/jobs` (list), `GET
    /api/v1/jobs/{id}` (job + last N log lines), `POST /api/v1/jobs` (body
    `{kind, args}` — only `echo` and `dry-build` allowed in M2; anything else → 400
    with `{error:"kind not supported"}`).
  - `internal/portal/templates/jobs.html` + `portal.Jobs`: server-rendered table of
    jobs. Single htmx button "New dry-build" that `POST`s to `/api/v1/jobs` and prepends
    a row via htmx OOB target.
  - Portal `GET /jobs` renders the table; `GET /partials/jobs` returns just rows.

  **Acceptance Criteria (TDD):**
  - [ ] `internal/server/server_test.go` (extend): `POST /api/v1/jobs {"kind":"echo",
    "args":["hello"]}` → 200 + `{id}`; poll `GET /api/v1/jobs/{id}` until `state ==
    "applied"`; assert log contains `hello`.
  - [ ] `go test ./internal/server/...` → PASS.

- [ ] **2.5 Cron wrapper (`robfig/cron/v3`)**

  **What to do:**
  - `internal/jobs/cron.go`: thin wrapper around `cron.New(cron.WithChain(
    cron.Recover(cron.DefaultLogger), cron.DelayIfStillRunning(cron.DefaultLogger)))`.
  - `AddFunc(spec, fn)` + `Start(ctx)`/`Stop()`. Used in M3 (GitOps poll) and M4
    (apply), but the wrapper exists in M2 and is unit-tested with `*/5 * * * *`
    firing a counter assertion.

  **Acceptance Criteria (TDD):**
  - [ ] `internal/jobs/cron_test.go`: register `@every 100ms` handler that increments a
    counter; assert it fires ≥2 times within 600ms; assert second invocation is
    **delayed** while the first sleeps 250ms (so total firings ≤2 in 350ms).
  - [ ] `go test ./internal/jobs/...` → PASS.

- [ ] **2.6 NixOS VM test `jobs-runner`**

  **What to do:**
  - `nix/checks/jobs-runner.nix`: a node running the nixmox module. Test:
    ```nix
    machine.succeed("curl -sf -XPOST http://127.0.0.1:8080/api/v1/jobs -d '{\"kind\":\"echo\",\"args\":[\"hi\"]}' -H 'Content-Type: application/json' | grep -q '\"id\"'");
    machine.wait_until_succeeds("curl -sf http://127.0.0.1:8080/api/v1/jobs | grep -q applied");
    ```
  - Expose as `checks.x86_64-linux.jobs-runner`.

  **Acceptance Criteria:**
  - [ ] `nix build .#checks.x86_64-linux.jobs-runner` exits 0.

### M2 Done-Criteria

```bash
go test ./... -race
./nixmox serve & PID=$!
sleep 0.5
ID=$(curl -sf -XPOST http://127.0.0.1:8080/api/v1/jobs -d '{"kind":"echo","args":["hello"]}' -H 'Content-Type: application/json' | jq -r .id)
# poll until applied
while [ "$(curl -sf http://127.0.0.1:8080/api/v1/jobs/$ID | jq -r .state)" != "applied" ]; do sleep 0.2; done
curl -sf http://127.0.0.1:8080/api/v1/jobs/$ID | grep -q hello
kill -TERM $PID
nix build .#checks.x86_64-linux.jobs-runner     # PASS
```

**Atomic commits:**
1. `feat(jobs): file-per-job store with atomic transitions`
2. `feat(jobs): state machine + table-driven transition tests`
3. `feat(jobs): goroutine runner with per-host lock + streaming logs`
4. `feat(server,portal): /api/v1/jobs + portal jobs view`
5. `feat(jobs): robfig/cron wrapper with Recover+DelayIfStillRunning`
6. `test(nix): jobs-runner nixosTest`

---

## M3 — GitOps pull (Wave 3)

**Goal:** configure the GitOps repo via portal/API, clone it into `{data_dir}/git/repo`,
run a cron `git fetch` + SHA compare, detect upstream changes ("update available"),
update the local clone on demand, and surface `repo URL / branch / last-pulled SHA /
update-available?` in the portal. **No apply in M3** — only the pull side.

**Two gitops repo modes (Amendment 3):**

- **Mode A — Automated nixosTest fixture (deterministic, hermetic):** the
  `nix/checks/gitops-pull.nix` test driver creates a bare git repo **inside the test
  network** (file:// path or a local `git-daemon`), pre-seeds an initial commit, and
  points the agent's `GitRepoURL` at it. The driver then fabricates "new commit"
  scenarios by writing to the bare repo — fully deterministic, no network egress. M3.5
  uses this mode.
- **Mode B — Interactive demo / real dogfooding (manual QA path):** the user enters
  the **real git remote URL** — either this repo's GitHub URL or their homelab config
  repo — through the **portal** (`PUT /api/v1/gitops`), and the agent clones/pulls it
  for real. DECISION (user, 2026-08-06): in the demo the portal is the ONLY gitops
  configuration path — demo VMs MUST ship with no repo URL in env/config (the
  `nixosConfigurations.node*` / demo VM modules must NOT seed `GitRepoURL`).
  Env/config-file seeding of `GitRepoURL` exists solely for Mode A automated tests and
  headless deployments. Authenticated private repos use SSH key / netrc from config
  (no portal key UI in v1). M7 docs treat this as the canonical manual QA path, and M8
  (interactive demo) uses it end-to-end.

Both modes share the same code path — only the `GitRepoURL` value differs. The
GitOps layer is mode-agnostic.

**Expected files/packages:**
- `internal/gitops/{client,poller,clone}.go` + tests
- `internal/server/handlers.go` (extend: `/api/v1/gitops` GET/PUT, `/api/v1/gitops/pull` POST)
- `internal/portal/templates/gitops.html` + `portal.GitOps`
- `nix/checks/gitops-pull.nix`

- [ ] **3.1 Git client interface (mockable)**

  **What to do:**
  - `internal/gitops/client.go`: `type Client interface { Clone(ctx, url, dir string)
    error; Fetch(ctx, dir string) error; RemoteHEAD(ctx, dir, branch string) (string,
    error); Pull(ctx, dir, branch string) error; CurrentSHA(ctx, dir string) (string,
    error) }`. Implementation via `exec.Command("git", ...)` with `-C dir` flags.
  - The interface is the seam for tests — the poller never invokes `git` directly.

  **Acceptance Criteria (TDD):**
  - [ ] `internal/gitops/client_test.go`: real `git` against a temp repo created with
    `exec` in `t.TempDir()` — init, commit, clone, fetch, assert `RemoteHEAD`
    matches.
  - [ ] `go test ./internal/gitops/...` → PASS.

- [ ] **3.2 Clone management (`{data_dir}/git/repo`)**

  **What to do:**
  - `internal/gitops/clone.go`: `type Manager struct { dir string; client Client; url,
    branch string }`. `Ensure()` clones if `{dir}` empty; else `Fetch()` + `Reset`
    `--hard origin/<branch>`. Stores `last-pulled SHA` in a small metadata file
    `{data_dir}/git/state.json` (so agent restarts know the baseline).
  - SSH / netrc auth: read `cfg.SSHKeyPath` (set `GIT_SSH_COMMAND=ssh -i <path>`) and
    `cfg.NetrcPath` (set `HOME` for netrc, or `GIT_CONFIG_GLOBAL`) into the
    `exec.Command.Env`. No portal UI to edit them — but the portal can show which is
    configured (read-only), per open-question #2's default.
  - On `Reset --hard`, warn in logs if there are unstaged changes (POC: log only, the
    agent owns the clone so this should not happen).

  **Acceptance Criteria (TDD):**
  - [ ] `internal/gitops/clone_test.go` using a temp bare repo + temp `Manager.Dir`:
    (a) `Ensure()` on empty dir → cloned, `state.json` written; (b) new commit on the
    upstream, `Ensure()` again → `last-pulled SHA` updated; (c) bad URL → error wraps
    with context.
  - [ ] `go test ./internal/gitops/...` → PASS.

- [ ] **3.3 Cron poller (SHA change detection)**

  **What to do:**
  - `internal/gitops/poller.go`: `type Poller struct { mgr *Manager; cron *Cron;
    interval time.Duration; onNew func(newSHA string) }`. Every `interval`: `Fetch()`,
    compare `RemoteHEAD` to `mgr.LastPulled()`. If different, record
    `"update_available = true"` in `state.json` and call `onNew` (M4 wires this to the
    auto-apply path; M3 leaves it as a no-op log).
  - Cron spec uses the M2 `jobs.cron.AddFunc` wrapper so `DelayIfStillRunning()`
    prevents overlap during a slow fetch.

  **Acceptance Criteria (TDD):**
  - [ ] `internal/gitops/poller_test.go` using a **mock** `Client`: (a) `RemoteHEAD`
    same as before → no `onNew` call; (b) changed → `onNew` fired with new SHA,
    `state.json.update_available == true`; (c) `Fetch` returns error → logged, no
    crash, next tick proceeds.
  - [ ] `go test ./internal/gitops/...` → PASS.

- [ ] **3.4 API + portal for GitOps config + status**

  **What to do:**
  - `internal/server/handlers.go`:
    - `GET /api/v1/gitops` → `{repo_url, branch, poll_interval, last_pulled_sha,
      update_available, ssh_key_path (if set), netrc_path (if set)}`
    - `PUT /api/v1/gitops` body `{repo_url, branch, poll_interval}` → updates config
      (in-memory + writes back to `/etc/nixmox/config.yaml` **only if it exists and
      the user opted in via `cfg.AllowConfigWrite=false` by default** — for POC default
      is read-only: PUT returns 405 with a hint to edit the config file; document this.
      Open-question #2 default resolves this). Actually keep it simple:
      `PUT /api/v1/gitops` is **allowed** when `cfg.AllowConfigWrite == true` (default
      false), else 405.
    - `POST /api/v1/gitops/pull` → triggers `mgr.Ensure()` immediately and returns the
      new `last_pulled_sha`.
  - `internal/portal/templates/gitops.html` + `portal.GitOps`: shows the status table
    + a "Pull now" htmx button + read-only SSH/netrc paths (if any).

  **Acceptance Criteria (TDD):**
  - [ ] `internal/server/server_test.go` (extend): `GET /api/v1/gitops` returns the
    JSON shape; `POST /api/v1/gitops/pull` with a mock-shaped gitops component returns
    new SHA (inject a fake `gitops.Manager` into the server via a small interface).
  - [ ] `go test ./internal/server/...` → PASS.

- [ ] **3.5 NixOS VM test `gitops-pull` (Mode A: hermetic fixture)**

  **What to do:**
  - `nix/checks/gitops-pull.nix`: **Mode A only** — the test driver fabricates the git
    source entirely inside the test net. Specifically:
    1. The driver creates a bare repo at `/srv/gitops-fixture.git` **inside the test
       VM** and seeds an initial commit (`git init --bare` + a non-bare workdir whose
       `defaultBranch` matches the configured `GitBranch`).
    2. nixmox is configured via the test's `services.nixmox.config.git.repoUrl =
       "file:///srv/gitops-fixture.git"` (no LAN git-daemon needed; `file://` is
       hermetic and works inside the QEMU net).
    3. Test script: bootstrap → `GET /api/v1/gitops` shows
       `last_pulled_sha != null && update_available == false`; commit to the bare
       repo's working tree (driver pushes a second commit) → poll/tick →
       `update_available == true`; `POST /api/v1/gitops/pull` → `last_pulled_sha`
       matches the new commit.
  - **No network egress from the VM** — all git operations are local file paths. This
    is what makes the test deterministic.
  - Expose as `checks.x86_64-linux.gitops-pull`.

  **Acceptance Criteria:**
  - [ ] `nix build .#checks.x86_64-linux.gitops-pull` exits 0.

### M3 Done-Criteria

```bash
go test ./... -race
# Use an empty local git repo as the GitOps source
REPO=$(mktemp -d); git init -q --bare "$REPO"
./nixmox serve & PID=$!  # with config: NIXMOX_GIT_REPO_URL=$REPO NIXMOX_GIT_BRANCH=main
sleep 0.5
curl -sf http://127.0.0.1:8080/api/v1/gitops       # JSON with repo_url == $REPO
curl -sf -XPOST http://127.0.0.1:8080/api/v1/gitops/pull   # last_pulled_sha empty / first commit
kill -TERM $PID
nix build .#checks.x86_64-linux.gitops-pull       # PASS
```

**Atomic commits:**
1. `feat(gitops): git Client interface + clone manager`
2. `feat(gitops): cron poller with SHA change detection`
3. `feat(server,portal): /api/v1/gitops + portal status + read-only auth paths`
4. `test(nix): gitops-pull nixosTest (Mode A hermetic file:// bare-repo fixture)`

---

## M4 — Apply (Wave 4)

**Goal:** issue a real `nixos-rebuild switch --flake <local-clone>#<hostname>` job,
emit a rich **ApplyRecord** (with `genBefore`/`genAfter`) on every terminal state,
support manual rollback with `genBefore` as the precise target, ship the self-restart
guards in the NixOS module, and validate with a `nixosTest` VM integration test
(baseline: `dry-activate`; stretch: real `switch` of a trivial config change).

**Technical prereqs (locked, restated):**
- NixOS module: `restartIfChanged=false; X-StopOnRemoval=false; KillMode=process`.
- Job runs in a goroutine; HTTP + heartbeats never block on it.
- Per-host one-apply-at-a-time enforced by the M2 `Runner` per-host lock.
- ApplyRecord (see "Design constraints & risks § 3") is emitted via the M2 runner's
  `OnTerminal` hook seam. M4 stores it **locally**; M5 mirrors it to KV.

- [ ] **4.1 `nixos-rebuild switch` job kind**

  **What to do:**
  - `internal/apply/apply.go`: `type Applier struct { runner *jobs.Runner; mgr
    *gitops.Manager; cfg *config.Config; rec *RecordStore }`.
    `Apply(ctx, host string) (jobID string, err error)`:
    1. **Capture `genBefore`** — read the current NixOS generation number BEFORE
       queueing the job (see § 3 for the parsing strategy:
       `/nix/var/nix/profiles/system` symlink target → highest N, or
       `nixos-rebuild list-generations --json` + take `current`).
    2. Enqueue a job with `kind = "apply"`, args `["switch", "--flake",
       "<dir>#<hostname>"]`, runs `exec.CommandContext("nixos-rebuild", args...)`,
       streams stdout/stderr line-by-line through the M2 runner log streamer.
    3. **Wire `runner.OnTerminal`** to: capture `genAfter` (re-read the highest
       generation after the job exits); resolve `gitSHA` per precedence in § 3;
       construct `ApplyRecord{timestamp: now, status: <applied|failed>, gitSHA,
       genBefore, genAfter}`; persist via `rec.Record(record)` (M4) and (no-op in M4)
       schedule the M5 KV mirror.
  - Job state machine transitions are the M2 ones: `pending → building → applying →
    applied | failed`. One kind (`switch`); M4.5 uses `dry-activate` as a safety
    variant for the VM check only.

  **Acceptance Criteria (TDD):**
  - [ ] `internal/apply/apply_test.go` with a mock `gitops.Manager` + fake
    `exec.LookPath("nixos-rebuild")` resolvable → the Applier constructs the right
    args and enqueues a job whose `kind == "apply"`, and `OnTerminal` was wired.
    (The runner itself is unit-tested in M2; here just verify the wiring.)
  - [ ] `go test ./internal/apply/...` → PASS.

- [ ] **4.2 ApplyRecord store + applylog history (local in M4, KV-ready seam)**

  **What to do:**
  - `internal/cluster/applylog.go`: `type RecordStore interface { Record(r
    ApplyRecord) error; Latest(nodeID string) (*ApplyRecord, error); History(nodeID
    string, limit int) ([]ApplyRecord, error) }`.
  - **`LocalRecordStore`** (M4, pre-cluster): persists the latest to
    `{data_dir}/applied.json` atomically (`.tmp` + `os.Rename`); appends a row to
    `{data_dir}/applylog/<nodeID>.jsonl` (one line per record, JSON); keeps the **last
    50** rows (rewrite-on-flush when exceeding 50 — rotate atomically).
  - **`ClusterRecordStore`** (M5, stub now): same interface, backed by the
    `hosts.<nodeID>` + `applylog.<nodeID>.<unix-ts>` KV layout described in § 3. M4
    ships the stub returning `ErrClusterNotReady`; M5 swaps it in.
  - `ApplyRecord` type lives in `internal/cluster/node.go` (shared definition across
    M4/M5).
  - Rollback target semantically equals `genBefore` of the *last successful* apply —
    the `Rollback` operation in 4.3 reads `rec.Latest(nodeID).GenBefore` to display
    "Rolling back to generation N" in the portal before issuing the job.

  **Acceptance Criteria (TDD):**
  - [ ] `internal/cluster/applylog_test.go`: (a) `Record` writes both
    `{data_dir}/applied.json` (latest) and appends to `<nodeID>.jsonl`; (b)
    `History(nodeID, 50)` returns newest first; (c) appending the 51st record prunes
    the oldest so `len(history) == 50` always; (d) on a simulated mid-write crash
    (kill write between `.tmp` create and rename) the next `Latest()` returns the
    prior record (not partial JSON); (e) `ClusterRecordStore` stub returns
    `ErrClusterNotReady`.
  - [ ] `go test ./internal/apply/... ./internal/cluster/...` → PASS.

- [ ] **4.3 Rollback job + portal apply/rollback buttons + host-detail view**

  **What to do:**
  - `internal/apply/apply.go`: `Rollback(ctx, host string) (jobID, err)` enqueues a job
    with `kind = "rollback"`, args `["switch", "--rollback"]`. State machine
    transition `applied --rollback--> building` is already in the M2 matrix. The
    portal's "Rollback" button displays `"Rolling back to generation {genBefore}"` by
    reading `rec.Latest(nodeID).GenBefore` of the last successful record before the
    POST issues (`GET /api/v1/hosts/{nodeID}` exposes the latest ApplyRecord).
  - `internal/server/handlers.go`: `POST /api/v1/hosts/{host}/apply` →
    `applier.Apply`; `POST /api/v1/hosts/{host}/rollback` → `applier.Rollback`. Both
    return the new `jobID` (HTTP 202 Accepted). If `ErrHostBusy` → 409. `GET
    /api/v1/hosts/{nodeID}` now also returns the latest ApplyRecord embedded in the
    host object; `GET /api/v1/hosts/{nodeID}/applylog` returns the last 50 records.
  - Portal host row gets two htmx buttons: "Apply" and "Rollback" (with the
    "→ generation N" hint). Buttons disabled when a job is currently running for that
    host (use the `/partials/hosts` polling).
  - **Host-detail view (`internal/portal/templates/host-detail.html`,
    `Portal.HostDetail`):** `GET /hosts/{nodeID}` renders the host's metadata plus a
    table of the applylog history (last 50, newest first, columns: timestamp, status,
    gitSHA, genBefore→genAfter, job ID links back to `/jobs/{id}`). Wired with htmx
    polling for live updates.

  **Acceptance Criteria (TDD):**
  - [ ] `internal/server/server_test.go` (extend): `POST /api/v1/hosts/.../apply` with
    a fake applier → 202 + `{id}`; concurrent apply → 409 on the second; `GET
    /api/v1/hosts/{nodeID}` returns the latest ApplyRecord after the fake apply
    completes; `GET /api/v1/hosts/{nodeID}/applylog` returns the history ordered
    newest-first.
  - [ ] `internal/portal/portal_test.go` (extend): `Portal.HostDetail` renders the
    applylog table with the timestamp, status, gens, and gitSHA columns present.
  - [ ] `go test ./internal/server/... ./internal/apply/... ./internal/portal/...` →
    PASS.

- [ ] **4.4 NixOS module: self-restart guards + persistence dir + applyMode**

  **What to do:**
  - `nix/module.nix`: extend the M0 stub:
    ```nix
    systemd.services.nixmox = {
      wantedBy = ["multi-user.target"];
      restartIfChanged = false;
      unitConfig.X-StopOnRemoval = false;
      serviceConfig = {
        KillMode = "process";
        StateDirectory = "nixmox";
        StateDirectoryMode = "0750";
        WorkingDirectory = "/var/lib/nixmox";
        ExecStart = "${pkg}/bin/nixmox serve";
        User = "root"; Group = "root";
      };
    };
    services.nixmox.openFirewall = mkOption { ... };  # opens 6222 + 8080 by default
    services.nixmox.applyMode = mkOption {
      type = types.enum ["switch" "dry-activate"];
      default = "switch";  # the VM test overrides to "dry-activate" for safety
    };
    ```
  - Document in `module.nix` comment: **WHY** `restartIfChanged=false` — otherwise
    nixmox's own unit change during `nixos-rebuild switch` kills it mid-apply.

  **Acceptance Criteria:**
  - [ ] `nix build .#nixosModules.default` builds; applying it to a VM yields a
        running `nixmox.service` with the documented guards (`systemctl cat nixmox` shows
        the three).

- [ ] **4.5 NixOS VM test `apply-switch`**

  **What to do:**
  - `nix/checks/apply-switch.nix`: `nixosTest`. Baseline variant:
    ```nix
    machine.succeed("curl -sf -XPOST http://127.0.0.1:8080/api/v1/hosts/machine/apply -d '{}' -H 'Content-Type: application/json' | grep -q '\"id\"'");
    machine.wait_until_succeeds("curl -sf http://127.0.0.1:8080/api/v1/jobs | jq -e 'any(.state==\"applied\")'");
    # ApplyRecord persisted end-to-end
    machine.wait_until_succeeds("curl -sf http://127.0.0.1:8080/api/v1/hosts/machine | jq -e '.apply_record.status=='\"success\"' and .apply_record.genAfter > .apply_record.genBefore'");
    machine.succeed("curl -sf http://127.0.0.1:8080/api/v1/hosts/machine/applylog | jq -e 'length >= 1'");
    ```
    Where the apply actually runs `nixos-rebuild dry-activate ...` because the test
    overrides `services.nixmox.applyMode = "dry-activate"` (added in 4.4) so the VM
    test is safe by default. The test asserts the job ended `applied` AND that the
    ApplyRecord's `genAfter >= genBefore` (a dry-activate may bump the generation by
    0 or 1, but never decrement). For the local store, also asserts
    `/var/lib/nixmox/applied.json` was written.
  - **Stretch goal (gate on feasibility noted in open-question #2):** real
    `switch` of a trivial config change (add `systemd.services.dummy-apply-test`).
    Assert the dummy unit appears after the apply and that `genAfter > genBefore`.
    If infeasible in the VM, keep `dry-activate` baseline and add a TODO comment.

  **Acceptance Criteria:**
  - [ ] `nix build .#checks.x86_64-linux.apply-switch` exits 0.

### M4 Done-Criteria

```bash
go test ./... -race
nix flake check --no-build    # all checks parse
nix build .#checks.x86_64-linux.apply-switch     # PASS
# In a real systemd unit:
systemctl cat nixmox | grep -q "KillMode=process"
systemctl cat nixmox | grep -q "X-StopOnRemoval=false"
# ApplyRecord persisted locally:
test -f /var/lib/nixmox/applied.json
test -f /var/lib/nixmox/applylog/$(hostname).jsonl
# Portal shows the record:
curl -sf http://127.0.0.1:8080/api/v1/hosts/$(hostname) | jq -e '.apply_record.status'
curl -sf http://127.0.0.1:8080/api/v1/hosts/$(hostname)/applylog | jq -e 'length >= 1'
```

**Atomic commits:**
1. `feat(apply): nixos-rebuild switch job kind with genBefore/genAfter capture`
2. `feat(cluster,apply): ApplyRecord store (local) + applylog history with N=50 prune`
3. `feat(apply,server,portal): rollback (genBefore target) + host-detail view with applylog`
4. `feat(nix): NixOS module self-restart guards + applyMode option + StateDirectory`
5. `test(nix): apply-switch nixosTest (dry-activate baseline, ApplyRecord assertions)`

---

## M5 — Clustering + portal-driven bootstrap (Wave 5)

**Goal:** embed NATS + JetStream per node via a **NATS supervisor** that supports
runtime reconfigure (so the portal setup screen — the M1 stub — becomes functional:
"Create new cluster" or "Join existing cluster"). Create KV buckets (`config`,
`hosts`) at R=1, auto-promote to R=3 once ≥3 JetStream peers. Heartbeats write to
`hosts` with TTL ~30s refreshed every ~10s — key expiry = offline detection. **Latest
ApplyRecord mirrors into `hosts.<nodeID>` and complete history into
`applylog.<nodeID>.<ts>` KV keys** (the M4 `ClusterRecordStore` stub now becomes a
real implementation). Portal shows the **cluster** view: every host, its online/offline
status, pulled SHA vs latest ApplyRecord (status, gitSHA, gens, timestamp), agent
version. KV watchers drive portal updates (`/partials/hosts`). `nixmox join <peer>` CLI
and the portal join screen call the **same internal join API**, so they never diverge.

**Critical-research notes (design around these):**
- Embedded server pattern: `server.Options{ServerName, Host, Port, JetStream: true,
  StoreDir, Cluster: {Name, Host, Port}, Routes}` → `server.NewServer(opts)` →
  `go srv.Start()` → `srv.ReadyForConnections(timeout)`. Shutdown:
  `srv.Shutdown()` then `srv.WaitForShutdown()`.
- **Cluster bootstrap twist (Amendment 5):** Routes are startup-only config, but the
  agent must support `Join` *while running*. The mechanism is a **NATS supervisor**:
  it owns the embedded-server lifecycle and can `Reconfigure(opts)` →
  `srv.Shutdown()` / `srv.WaitForShutdown()` → re-`NewServer` with new
  `Cluster.Name` + `Routes` → `srv.Start()` / `ReadyForConnections`. Crucially, the
  **HTTP server and job runner keep running** during this swap (NATS restart is the
  only thing that happens). The in-process `nats` client reconnects to the new
  `127.0.0.1` listen port, re-creates the JetStream context, re-runs meta-leader
  poll, re-emits a first heartbeat immediately.
- Cluster join (no auth, trusted LAN): joining node's agent POSTs to the **peer's
  HTTP API** (`POST /api/v1/cluster/join` carrying its own
  `{nodeID, hostname, clusterAddr}`); the peer **validates the cluster name matches**
  the joining payload (prevents accidentally merging two clusters — the joining node
  got the cluster name from the user's `host:port` lookup response, not from guess),
  records the new member, and returns `{cluster_name, known_peers}`. The joining node
  then reconfigures NATS routes to the seed; gossip forms the mesh. If the cluster
  names don't match, the join is rejected with HTTP 409 Conflict.
- Single node = supervisor starts NATS with **no `Cluster` block** → works
  standalone. "Create new cluster" just stamps the `ClusterName` in `node.json` and
  **only reconfigures if the user supplied a writable advertised address** (LAN IP);
  otherwise the cluster is "name-only, single-node" and the R=1 KV buckets suffice.
- **MUST wait for JetStream meta-leader** before `CreateKVBucket`; meta-leader
  election takes ~4–9s. Poll `srv.JetStreamIsLeader()` /
  `JetStreamClusterPeers()` with a timeout (plan: poll every 500ms up to 30s, fail
  the startup with a clear error otherwise).
- **R=2 forbidden.** R=1 at bootstrap; R=3 once ≥3 JS peers. Update via
  `js.UpdateStream` (no downtime, supported ≥ 2.9).
- **AllowDirect pitfall:** KV buckets default `AllowDirect=true` → followers serve
  stale reads. For the `config` bucket (apply decisions need coherence), and
  equally for `hosts.<nodeID>` (the portal must not show stale ApplyRecords), prefer
  `AllowDirect=false` for both buckets.
- Pin `nats-server/v2` **≥ v2.10.19**.

**Expected files/packages:**
- `internal/natsx/{embed,kv}.go` + tests
- `internal/cluster/{supervisor,join,heartbeat,applylog-remote,scalereplicas}.go` + tests
- `internal/server/handlers.go` (extend: portal cluster view functional, `/api/v1/cluster/{create,join}`, `/api/v1/bootstrap` reflects real cluster state, `/api/v1/hosts` returns all + embedded ApplyRecord, `/api/v1/hosts/{nodeID}/applylog` reads from KV)
- `cmd/nixmox/join.go` (real CLI now calling the same internal join API as the portal)
- `nix/checks/cluster-join.nix`

- [ ] **5.1 Embedded NATS lifecycle primitives (`internal/natsx/embed.go`)**

  **What to do:**
  - `type EmbeddedServer struct { srv *server.Server; opts server.Options; js
    nats.JetStreamContext }`. Pure primitives — this struct exposes `Start(opts)`,
    `Stop(ctx)`, and `ReadyForConnections(timeout)`; it does NOT decide when to
    reconfigure (that's the supervisor's job in 5.3).
  - `NewNATSOptions(cfg, dataDir, cluster ClusterSpec)` constructs `server.Options`:
    `ServerName = cfg.Hostname`, `Host = cfg.NATSHost`, `Port = cfg.NATSPort`,
    `JetStream: true`, `StoreDir = filepath.Join(dataDir, "nats")`, and conditionally
    `Cluster.Name = cluster.Name`, `Cluster.Host/Port`, `Routes = []*url.URL{...}` —
    `Cluster` field is omitted entirely when `cluster.Name == ""` (standalone mode).
  - `Start(ctx)` runs `go srv.Start()`, waits `srv.ReadyForConnections(10s)`,
    connects the in-process `nats` client (`nats.Connect("nats://127.0.0.1:<port>")`),
    gets `JetStreamContext`. **Then** polls `srv.JetStreamIsLeader()` (or
    `nats.JetStreamAccountInfo`) up to 30s for meta-leader readiness. Returns the
    `JetStreamContext` to the caller.
  - `Stop(ctx)` calls `srv.Shutdown()` + `srv.WaitForShutdown()` honoring ctx.

  **Acceptance Criteria (TDD):**
  - [ ] `internal/natsx/embed_test.go`: start embedded server on an ephemeral port +
    ephemeral `StoreDir` (`t.TempDir()`) with NO cluster block (standalone mode);
    assert `ReadyForConnections` and that a KV bucket can be created (→ meta-leader
    ready within the poll window). Also assert the standalone-mode options have
    `Cluster.Name == ""`.
  - [ ] `go test ./internal/natsx/...` → PASS.

- [ ] **5.2 KV bucket helpers + meta-leader wait (`internal/natsx/kv.go`)**

  **What to do:**
  - `EnsureKV(js, name string, cfg BucketConfig) (nats.KeyValue, error)`: idempotent —
    `js.KeyValue` first; `ErrBucketNotFound` → `js.CreateKeyValue(&nats.KeyValueConfig{
    Bucket: name, Replicas: 1, TTL: cfg.TTL, History: 1, AllowDirect: cfg.AllowDirect
    /* false by default for both `config` and `hosts` buckets */})`. Append `Replay
    airborne` etc. as needed.
  - `PollForLeader(ctx, srv, timeout time.Duration) error`: loop described above.
  - `ScaleReplicas(js, name string, replicas int)`: `js.UpdateStream(&nats.StreamConfig{
    Name: fmt.Sprintf("KV_%s", name), Replicas: replicas})`. Only call when peer count
    ≥ replicas.

  **Acceptance Criteria (TDD):**
  - [ ] `internal/natsx/kv_test.go`: (a) `EnsureKV` creates once, returns same KV on
    second call; (b) `ScaleReplicas` from 1→1 is a no-op; (c) `PollForLeader` returns
    an error when the timeout elapses with no leader (use a sandboxed server with no
    JetStream to simulate — or just test the timeout branch via a fake `srv`-shaped
    interface).
  - [ ] `go test ./internal/natsx/...` → PASS.

- [ ] **5.3 NATS supervisor with RUNTIME RECONFIGURE (`internal/cluster/supervisor.go`)**

  **What to do:**
  - `type Supervisor struct { current *natsx.EmbeddedServer; cfg *config.Config;
    dataDir string; mu sync.RWMutex }`.
  - `Start(ctx)` constructs an `EmbeddedServer` in **standalone mode** (no `Cluster`
    block, `cluster.Name == ""`) and starts it. The agent always boots unclustered;
    the portal setup screen (M1 stub made functional here) drives the transition.
  - `CreateCluster(ctx, name string, advertisedAddr string) error`:
    1. Stamp `cluster_name = name` in `{data_dir}/node.json`.
    2. `Reconfigure(ClusterSpec{Name: name, Routes: nil})` — NATS restarts with
       `Cluster.Name = name` and no routes (single-node cluster). If
       `advertisedAddr != ""`, also set `Cluster.Host/Port` accordingly.
    3. After NATS ready, `EnsureKV("config", …)` and `EnsureKV("hosts", …)` at R=1.
    4. Mark self as the first cluster member in the `hosts.<selfNodeID>` KV.
  - `JoinCluster(ctx, seedPeerClusterURL string) (joinedClusterName string, err error)`:
    1. Resolve the seed peer's HTTP URL (peer advertises both `host:6222` for NATS and
       `host:8080` for HTTP — convert `nats://host:6222` to `http://host:8080`).
    2. `GET http://<seed>:8080/api/v1/bootstrap` to read the peer's cluster name.
    3. `POST http://<seed>:8080/api/v1/cluster/join` with
       `{nodeID, hostname, clusterAddr, target_cluster_name: <peer's name>}`.
    4. The peer validates `target_cluster_name == self.ClusterName` — returns 409
       Conflict if mismatch (prevents accidental cluster merge); on success returns
       `{cluster_name, known_peers}`.
    5. Stamp `cluster_name` + `routes=[seedPeerNATSURL, ...knownPeers]` into
       `node.json`.
    6. `Reconfigure(ClusterSpec{Name: <name>, Routes: [...routes]})` — NATS restarts
       with the seed route; gossip forms the mesh.
    7. Re-run `PollForLeader`; KV buckets already exist from peers, just latch on.
  - `Reconfigure(spec ClusterSpec)` — the heart of the supervisor. With the mutex
    held: `srv.Shutdown() → srv.WaitForShutdown() → NewEmbeddedServer(newOpts) →
    Start() → Ready + PollForLeader`. The HTTP server, jobs runner, and cron are NOT
    touched — only NATS. Document this explicitly in code comments.

  **Must NOT do:**
  - Cluster renames are forbidden — `cluster_name` is set once at create- or join-
    time and immutable thereafter (documented in v1 out-of-scope).
  - `Reconfigure` must NOT kill in-flight `nixos-rebuild` jobs — they continue
    independent of NATS.

  **Acceptance Criteria (TDD):**
  - [ ] `internal/cluster/supervisor_test.go`: (a) `Start` standalone, assert
    `ClusterState.Status == "unclustered"`; (b) `CreateCluster("homelab")` →
    reconfigure happens in-process, `ClusterState.Status == "clustered"` +
    `ClusterName == "homelab"`; (c) simulate peer-end by standing up a second
    agent's HTTP API (+ stubbed `/api/v1/cluster/join` returning the right response)
    → `JoinCluster` walks through the steps, the second NATS instance forms a
    2-node mesh (assert `JetStreamClusterPeers()` length 2 within a timeout); (d)
    HTTP server keeps answering `/health` continuously during `Reconfigure` (spawn 5
    concurrent `GET /health` calls in goroutines, assert all return 200).
  - [ ] `go test ./internal/cluster/... -race` → PASS.

- [ ] **5.4 Internal join API + `nixmox join <peer>` CLI parity (`internal/cluster/join.go`)**

  **What to do:**
  - `type JoinRequest struct { NodeID, Hostname, ClusterAddr, TargetClusterName string
    }` and `type JoinResponse struct { ClusterName string; KnownPeers []string }`.
  - `POST /api/v1/cluster/join` server-side handler (in `internal/server/handlers.go`):
    validate `req.TargetClusterName == localClusterName` (else 409 with
    `{"error":"cluster name mismatch"}`); append `req.NodeID` to the local member
    list (a `members.<nodeID>` KV key in the `config` bucket — naming choice for
    discoverability); respond `JoinResponse` listing all currently-known peers.
  - `cmd/nixmox/join.go`: parses `<peer>` (`http://host:8080` — accepts either the
    HTTP form or strips/inserts the port from `nats://host:6222` for ergonomics),
    calls `JoinCluster` on the local running agent's supervisor through an
    in-process path **OR** (simpler and matching the portal) by `POST`ing to the
    local agent's own `/api/v1/cluster/join` endpoint. **CRITICAL:** the CLI and the
    portal MUST call the same internal function (`internal/cluster.JoinCluster`),
    so they never diverge. The join handler on the *peer* is shared code; the join
    *initiator* code is shared `JoinCluster`. Document with an explicit comment.

  **Acceptance Criteria (TDD):**
  - [ ] `internal/server/server_test.go` (extend): mock the cluster supervisor; `POST
    /api/v1/cluster/join` with matching cluster name → 200 + `JoinResponse`; with
    mismatching name → 409.
  - [ ] `internal/cluster/join_test.go`: `JoinCluster` calls the peer's HTTP join
    endpoint with `TargetClusterName` set (mock HTTP via `httptest.NewServer`); on
    409 it returns `ErrClusterNameMismatch`.

- [ ] **5.5 Introduce the unclustered setup screen + `/api/v1/bootstrap` (both new here)**

  **What to do:**
  - NEW in M5 (deferred from M1 — see M1.1 decision note): `setup.html` template with
    two choices — "Create new cluster" (name input) and "Join existing cluster"
    (`host:port` input) — plus `GET /api/v1/bootstrap`, a live view fed by the
    supervisor returning `{status: "unclustered"|"clustered", cluster_name, member_count}`.
    `Portal.Index` renders `setup.html` when unclustered, `portal.html` when clustered.
  - `POST /api/v1/cluster/create` body `{name, advertised_addr?}` →
    `supervisor.CreateCluster`; returns 200 + updated ClusterState.
  - Dual-route naming (avoid collision): the PEER-SIDE handler listens on
    `POST /api/v1/cluster/join` and is invoked by other nodes during their join; the
    portal's "Join" button on this node calls `POST /api/v1/cluster/join/initiate`
    with `{seed_peer_cluster_url}`, which does the outbound POST to the peer. Document
    this dual-route naming in code.
  - Setup screen's two buttons are active from birth: "Create new cluster" `POST`s
    `/api/v1/cluster/create`; "Join existing cluster" `POST`s
    `/api/v1/cluster/join/initiate`. On success, htmx swaps the setup screen for the
    cluster view (`portal.html`).

  **Acceptance Criteria (TDD):**
  - [ ] `internal/server/server_test.go` (extend): `GET /api/v1/bootstrap` with a
    mocked supervisor returns `{status:"unclustered"}` pre-create and
    `{status:"clustered", cluster_name:"homelab", member_count:1}` post-create.
  - [ ] `internal/portal/portal_test.go`: after a successful create, `Portal.Index`
    renders `portal.html` (host table) instead of `setup.html`.

- [ ] **5.6 Heartbeats + TTL offline detection + ApplyRecord KV mirror**

  **What to do:**
  - `internal/cluster/heartbeat.go`: `type Heartbeater struct { js nats.JS; rec
    RecordStore; nodeID, hostname string; interval 10s; ttl 30s }`. Loop: every
    `interval`, build the host payload (the `Host` row plus embedded latest
    ApplyRecord from `rec.Latest(nodeID)`) and `kv.Put(fmt.Sprintf("hosts.%s",
    nodeID), payload)`. KV bucket `hosts` TTL = `ttl`. On a new ApplyRecord arriving
    (M4 emits locally; the supervisor subscribes), the heartbeat **also** writes
    `applylog.<nodeID>.<unix-ts>` immediately (don't wait for the next tick) and
    prunes to N=50 per host.
  - If `Put` fails 3× in a row, log loudly (don't crash — daemon keeps serving local
    requests; standalone liveness matters more than cluster).
  - KV watcher: `Watch("hosts.*")` → on delete, mark host offline in the portal's
    in-memory store; on update, refresh the host row.

  **Acceptance Criteria (TDD):**
  - [ ] `internal/cluster/heartbeat_test.go` with an embedded NATS from 5.1: (a)
    `Put` fires every tick under test and embeds the latest ApplyRecord; (b) after
    `rec.Record(newRecord)` the applylog KV key is appended within 1 tick and the
    `hosts.<nodeID>` payload embeds the new latest; (c) after stopping the
    heartbeater, the `hosts.<nodeID>` KV key disappears within `ttl + small slack`;
    (d) watcher sees the delete; (e) after 51 records are written, only 50 applylog
    keys remain.

- [ ] **5.7 `ClusterRecordStore` (M4 stub becomes real; KV-backed applylog history)**

  **What to do:**
  - `internal/cluster/applylog.go` (extend the M4 `RecordStore` interface here): the
    `ClusterRecordStore` impl writes `applylog.<nodeID>.<unix-ts>` to the `hosts` KV
    bucket with TTL `30d` and updates `hosts.<nodeID>`'s embedded latest record. `Latest`
    reads `hosts.<nodeID>` and unmarshals the embedded `ApplyRecord` field; `History`
    lists keys with prefix `applylog.<nodeID>.` and decodes them.
  - **Active store switchover:** M4 used `LocalRecordStore`; M5 the Applier receives
    its `RecordStore` from a factory that picks `ClusterRecordStore` when the
    supervisor state is `clustered`, else `LocalRecordStore`. Both share the same
    interface, so M4 code is untouched.

  **Acceptance Criteria (TDD):**
  - [ ] `internal/cluster/applylog_test.go` (extend): with an embedded NATS,
    `Record(r)` writes both the applylog KV key AND updates `hosts.<nodeID>`;
    `History(nodeID, 50)` returns newest-first; 51st write prunes to 50.

- [ ] **5.8 Replica auto-scaling (R=1 → R=3 at ≥3 peers)**

  **What to do:**
  - `internal/cluster/scalereplicas.go`: periodic (cron, every 1m) check
    `srv.JetStreamClusterPeers()` (or `js.AccountInfo().Cluster`); if `peerCount >=
    3` AND any bucket is `Replicas == 1`, call `natsx.ScaleReplicas(js, name, 3)` for
    each bucket. If `peerCount < 3` AND any bucket is `Replicas == 3`, leave it (do
    NOT scale down in POC — R=3 with peers short of 2 quorum is tolerated better than
    re-scaling flapping).
  - Log every scaling decision.
  - **NEVER** use R=2 (assert `replicas != 2` in the helper; panic in tests if hit).

  **Acceptance Criteria (TDD):**
  - [ ] `internal/cluster/scalereplicas_test.go`: (a) `peerCount = 2` → no-op; (b)
    `peerCount = 3` → `UpdateStream` called once per bucket with `Replicas=3`; (c)
    `ScaleReplicas(_, _, 2)` panics (table test).
  - [ ] `go test ./internal/cluster/... -race` → PASS.

- [ ] **5.9 Portal cluster view + watcher-driven partials + host-detail from KV**

  **What to do:**
  - Portal `portal.html` (extend to multi-host): table of **all** known hosts (cluster
    in-memory store fed by KV watcher), with online/offline badge, pulled SHA,
    latest ApplyRecord (status, gitSHA, genBefore→genAfter, timestamp), agent version.
  - htmx polls `/partials/hosts` every 5s; the server merges KV-snapshot + watcher
    updates + local `LocalHost` row. `/partials/hosts/<nodeID>/applylog` returns the
    per-host applylog fragment for the host-detail view's polling.
  - `/api/v1/hosts` returns the full list with embedded ApplyRecord per host; `GET
    /api/v1/hosts/{nodeID}/applylog` reads from the KV (cluster mode) or local file
    (standalone fallback — M4's LocalRecordStore still serves when unclustered).
  - Validate-command shows 3 nodes after `CreateCluster` on node1 + `JoinCluster` by
    node2 and node3.

  **Acceptance Criteria (TDD):**
  - [ ] `internal/server/server_test.go`: with a fake cluster store, `GET
    /api/v1/hosts` returns 3 hosts each embedding the latest ApplyRecord; watcher's
    delete callback removes 1, next `GET` returns 2.

- [ ] **5.10 NixOS VM test `cluster-join`**

  **What to do:**
  - `nix/checks/cluster-join.nix`: 3 nodes (`node1`, `node2`, `node3`), all running
    the nixmox module via the M0 `nixosConfigurations.nodeN`. Test orchestrates the
    **portal-driven bootstrap** path (no `nixmox join` CLI directly; covers the same
    code though):
    ```nix
    # node1 unclustered → user hits "Create new cluster":
    node1.succeed("curl -sf -XPOST http://127.0.0.1:8080/api/v1/cluster/create -d '{\"name\":\"homelab\"}' -H 'Content-Type: application/json' | grep -q '\"status\":\"clustered\"'");
    # node2 unclustered → user hits "Join existing cluster":
    node2.succeed("curl -sf -XPOST http://127.0.0.1:8080/api/v1/cluster/join/initiate -d '{\"seed_peer_cluster_url\":\"http://node1:8080\"}' -H 'Content-Type: application/json' | grep -q '\"cluster_name\":\"homelab\"'");
    node3.succeed("curl -sf -XPOST http://127.0.0.1:8080/api/v1/cluster/join/initiate -d '{\"seed_peer_cluster_url\":\"http://node1:8080\"}' -H 'Content-Type: application/json'");
    node1.wait_until_succeeds("curl -sf http://127.0.0.1:8080/api/v1/hosts | jq -e 'length == 3'");
    # R-promotion assertion (asserts KV hosts stream has replicas: 3):
    node1.succeed("nixmox status");  # prints all 3 hosts
    ```

  **Acceptance Criteria:**
  - [ ] `nix build .#checks.x86_64-linux.cluster-join` exits 0.

### M5 Done-Criteria

```bash
go test ./... -race
nix build .#checks.x86_64-linux.cluster-join           # PASS
# Inside that test: GET /api/v1/hosts == 3 hosts online; each entry embeds the latest
# ApplyRecord; R==3 confirmed via debug JSON; portal setup screen renders only when
# unclustered.
```

**Atomic commits:**
1. `feat(natsx): embedded NATS lifecycle primitives (standalone + cluster modes)`
2. `feat(natsx): KV bucket helpers + R-scaling (R=1→R=3, never R=2)`
3. `feat(cluster): NATS supervisor with runtime reconfigure`
4. `feat(cluster,server): internal join API + cluster create/join endpoints; CLI parity`
5. `feat(portal): functional setup screen (create/join) replacing M1 stub`
6. `feat(cluster): heartbeat loop + KV-TTL offline detection + ApplyRecord mirror + watchers`
7. `feat(cluster): ClusterRecordStore (KV-backed applylog history with N=50 prune)`
8. `feat(cluster): replica auto-scaling cron task`
9. `feat(portal): cluster view (all hosts, online/offline, ApplyRecord per host)`
10. `test(nix): cluster-join 3-node nixosTest via portal bootstrap`

---

## M6 — Remote actions (Wave 6)

**Goal:** shutdown/reboot a remote host via NATS request-reply to
`nixmox.cmd.<nodeID>`; portal buttons issue these commands; a host deregisters its
heartbeat before `systemctl poweroff` so peers see it go gracefully; VM test covers
shutdown + reboot + recovery across 2–3 nodes.

**Expected files/packages:**
- `internal/cluster/cmd.go` + tests (NATS req/reply handlers)
- `internal/apply/apply.go` or `internal/cluster/cmd.go` (shutdown/reboot executor)
- `internal/server/handlers.go` (extend: `POST /api/v1/hosts/{host}/{shutdown|reboot}`)
- `internal/portal/templates/portal.html` (action buttons)
- `nix/checks/remote-actions.nix`

- [ ] **6.1 NATS command subject handler (`nixmox.cmd.<nodeID>`)**

  **What to do:**
  - `internal/cluster/cmd.go`: `type Cmd struct { Op string /*"shutdown"|"reboot"*/;
    Args map[string]string }`. Encoded JSON over core NATS request-reply subject
    `nixmox.cmd.<nodeID>`. Each node subscribes to its own `nodeID` subject via
    `nc.Subscribe`.
  - Handler: validate op (else 400), enqueue a `jobs` kind `"power"`, args
    `[op]`. Job executor calls `exec.Command("/sbin/shutdown", ...)`.
  - Reply: `{jobID, status: "queued"}` immediately (the actual poweroff is async; the
    daemon dies as part of shutdown).

  **Must NOT do:** cluster-wide broadcast / fanout; only 1:1 to the target node.

  **Acceptance Criteria (TDD):**
  - [ ] `internal/cluster/cmd_test.go`: embedded NATS, send `shutdown` to the
    node's subject, assert reply has `jobID`; assert invalid op → 400 body.

- [ ] **6.2 Shutdown/reboot executor with pre-deregister**

  **What to do:**
  - Power job executor: before `exec.Command("/sbin/shutdown", "-h", "now")` runs,
    call `heartbeater.Deregister()` (delete the KV key immediately so peers don't
    wait for TTL) and flush logs.
  - Reboot uses `/sbin/shutdown -r now`.
  - The HTTP server MUST continue serving the reply; the actual `exec.Command` runs
    in the M2 `Runner` goroutine, so the reply is already sent.

  **Acceptance Criteria (TDD):**
  - [ ] `internal/cluster/cmd_test.go` (extend): heartbeater's `Deregister` called
    before `exec` (intercept via interface + mock).

- [ ] **6.3 Portal + API action buttons**

  **What to do:**
  - `POST /api/v1/hosts/{host}/shutdown`, `POST /api/v1/hosts/{host}/reboot` → both
    publish the NATS request-reply. **The endpoint runs on whatever node the user's
    browser hits** (gateway pattern), forwards via NATS to the target. Returns 202 +
    `{jobID}` or 404 if the host is unknown / offline.
  - Portal buttons appear on each host row; confirm dialog (htmx `hx-confirm`) before
    firing.
  - Disable buttons for the host that the request is **served from** (self-shutdown
    from your own portal is allowed but with a strong confirm text — surfacing the
    local host's identity so the user doesn't accidentally brick their gateway).

  **Acceptance Criteria (TDD):**
  - [ ] `internal/server/server_test.go` (extend): fake the cluster `CmdForwarder`
    interface; `POST /api/v1/hosts/node2/shutdown` → 202 + `{jobID}`; unknown host → 404.

- [ ] **6.4 NixOS VM test `remote-actions`**

  **What to do:**
  - `nix/checks/remote-actions.nix`: 2–3 nodes. Test (sketch):
    ```nix
    node2.wait_for_unit("nixmox.service");
    node1.succeed("curl -sf -XPOST http://127.0.0.1:8080/api/v1/hosts/node2/reboot -d '{}' -H 'Content-Type: application/json' | grep -q '\"id\"'");
    # node2's KV key removed (graceful deregister):
    node1.wait_until_succeeds("curl -sf http://127.0.0.1:8080/api/v1/hosts | jq -e 'any(.hostname==\"node2\" and .status==\"offline\")'");
    # then node2 reboots and reappears:
    node2.wait_for_unit("nixmox.service");
    node1.wait_until_succeeds("curl -sf http://127.0.0.1:8080/api/v1/hosts | jq -e 'any(.hostname==\"node2\" and .status==\"online\")'");
    ```

  **Acceptance Criteria:**
  - [ ] `nix build .#checks.x86_64-linux.remote-actions` exits 0.

### M6 Done-Criteria

```bash
go test ./... -race
nix build .#checks.x86_64-linux.remote-actions     # PASS
# Inside the VM check: peer-driven reboot re-registers and the host returns online.
```

**Atomic commits:**
1. `feat(cluster): NATS cmd.<nodeID> request-reply handler`
2. `feat(cluster): shutdown/reboot executor with pre-deregister`
3. `feat(server,portal): /api/v1/hosts/{host}/{shutdown,reboot} actions`
4. `test(nix): remote-actions 2-3 node nixosTest`

---

## M7 — Remove + hardening + docs (Wave 7)

**Goal:** remove a host from the cluster via portal (tombstone + replica rebalance,
and leave the local node's agent intact), recover from agent restart/rejoin (re-emits
identity, rejoins gossip, heartbeat resumes), final `full-suite` VM test (join →
apply → remote action → remove), and finalize `DESIGN.md` / `AGENTS.md` / ADRs.

**Expected files/packages:**
- `internal/cluster/remove.go` + tests
- `internal/cluster/recover.go` (restart/rejoin path) + tests
- `nix/checks/full-suite.nix`
- `DESIGN.md`, `AGENTS.md`, `docs/adr/*.md`

- [ ] **7.1 Host removal (tombstone + rebalance + applylog archival)**

  **What to do:**
  - `internal/cluster/remove.go`: `Remove(nodeID)`: (a) tombstone the host in the
    `hosts` KV — write a row `hosts.<id>` with `status: "removed"`
    (TTL extended to e.g. 1h so portal shows history); (b) similarly stamp the
    `applylog.<nodeID>.*` keys' TTLs down to 1d so the history ages out faster once
    the host is gone (no need to keep 30d once the host is removed); (c) trigger the
    M5 scale-replicas task to reassess (since peer count dropped, R=3 stays but the
    peer distribution needs rebalancing — `js.UpdateStream` with `Placement` hints OR
    rely on NATS to re-elect the peer set automatically; document the choice).
  - `POST /api/v1/hosts/{host}/remove` (POST rather than DELETE to keep the
    auth-no-op middleware simple; same handler is reachable from the portal host
    row's "Remove" button) tombstones the host; returns 202.

  **Acceptance Criteria (TDD):**
  - [ ] `internal/cluster/remove_test.go`: with 3-node embedded NATS, removing a
    host → tombstone present, `peerCount` reflects 2 after the peer leaves; R stays
    3 (NATS tolerates this) → no crash; the removed host's `applylog.<nodeID>.*` keys
    exist with reduced TTL; subsequent heartbeat from a re-added host is accepted.

- [ ] **7.2 Agent restart / rejoin recovery (via NATS supervisor)**

  **What to do:**
  - `internal/cluster/recover.go`: on `serve` startup, the NATS supervisor (M5.3)
    reads `{cluster_name, routes}` from `node.json` and **starts NATS in the correct
    mode directly** — if `cluster_name` is present, `Start` is bypassed in favor of
    `Reconfigure(ClusterSpec{Name: cluster_name, Routes: routes})`. Re-emits the
    FIRST heartbeat immediately (no 10s wait), rebuilds the in-memory host view from
    a `js.Keys("hosts.*")` snapshot, and re-arms the applylog watcher.
  - If the persisted `Routes` point at a dead peer: log + try them in order; NATS
    gossip still finds the cluster if any one peer is reachable. Document this.
  - Edge case: a node that was unclustered when it shut down reboots — supervisor
    starts standalone (no routes), `ClusterState.Status=="unclustered"`, portal setup
    screen re-appears. Document.

  **Acceptance Criteria (TDD):**
  - [ ] `internal/cluster/recover_test.go`: stop the embedded NATS on node2; restart
    it via the supervisor's bootstrap path; assert `srv.ReadyForConnections` + first
    heartbeat within 2s of NATS ready. Also: node that had no `cluster_name` on disk
    → starts standalone and the portal renders `setup.html`.

- [ ] **7.3 Full VM suite (`full-suite`)**

  **What to do:**
  - `nix/checks/full-suite.nix`: 3 nodes (`node1/2/3`) using the M0
    `nixosConfigurations.nodeN`. Test script chains:
    1. `node1` unclustered → portal renders `setup.html` → `POST
       /api/v1/cluster/create {name:"homelab"}` → `GET /api/v1/hosts` length 1.
    2. `node2`, `node3` unclustered → portal bootstrap →
       `POST /api/v1/cluster/join/initiate {seed_peer_cluster_url:"http://node1:8080"}`
       → `GET /api/v1/hosts` length 3, all online.
    3. R stays 1 until 3rd joins, then asserts KV `hosts` stream has `replicas: 3`
       (use `nats kv info` equivalent — or `js.StreamInfo("KV_hosts")`).
    4. Patch a trivial config commit (Mode A fixture inside the test net) → `node1`
       GitOps poll sees "update available" → `node1` auto-apply if enabled; verify
       ApplyRecord's `genAfter > genBefore` and `gitSHA` matches the new commit;
       `GET /api/v1/hosts/node1/applylog` returns the persisted entry.
    5. `POST /api/v1/hosts/node2/reboot` → node2 goes offline then online (per M6
       VM test), resumes heartbeat, latest ApplyRecord stays visible.
    6. `POST /api/v1/hosts/node3/remove` → node3 tombstoned, `hosts` length drops to
       2 online; node3's applylog TTL reduced to 1d.
    7. Restart node3's service → recovery path: `cluster_name` still on disk →
       supervisor re-joins, re-appears online (M7.2 validation).
  - Expose as `checks.x86_64-linux.full-suite`.

  **Acceptance Criteria:**
  - [ ] `nix build .#checks.x86_64-linux.full-suite` exits 0.

- [ ] **7.4 Docs: `DESIGN.md`, `AGENTS.md`, ADRs (covering demo + two gitops modes + portal bootstrap)**

  **What to do:**
  - `DESIGN.md`: architecture overview, accepted risks (no-auth, R-scaling,
    ApplyRecord schema + sources, supervisor runtime reconfigure, two gitops modes,
    portal-driven bootstrap UX), persistence dir layout, the self-restart guards
    with rationale.
  - `AGENTS.md`: how to add a new portal page; how to add a new job kind; how to add a
    new NATS KV bucket; how to bump the version; how to add a new demo VM
    (nixosConfigurations); how to add a new nixosTest check; the test cascade (`go
    test`, `nix flake check`, `nix run .#demo`).
  - `docs/adr/0001-no-auth-poc.md`, `docs/adr/0002-embedded-nats.md`,
    `docs/adr/0003-jobs-as-files.md`,
    `docs/adr/0004-apply-record-schema.md` (ApplyRecord as the rich v1 source of
    truth, with `genBefore`/`genAfter` and the precedence-locked gitSHA resolution),
    `docs/adr/0005-portal-cluster-bootstrap.md` (NATS supervisor runtime reconfigure;
    cluster name immutability; trusted-LAN handshake with 409 on mismatch).
  - Update the root `README.md` quickstart with: `nix build .#default` + `nixmox
    serve` + portal URL; the **two** validation paths — `nix build
    .#checks.x86_64-linux.full-suite` (automated VM suite) and `nix run .#demo`
    (interactive click-through, see M8) — and the two gitops repo modes (test
    fixture vs real remote URL via portal).

  **Acceptance Criteria:**
  - [ ] `nix flake check` builds the docs linkcheck (if any; or just verify the files
        exist).
  - [ ] `ls docs/adr/*.md DESIGN.md AGENTS.md` non-empty.

### M7 Done-Criteria

```bash
go test ./... -race
nix build .#checks.x86_64-linux.full-suite       # PASS
ls docs/adr/*.md DESIGN.md AGENTS.md             # all exist
make lint                                        # exit 0
```

**Atomic commits:**
1. `feat(cluster): host removal (tombstone + applylog TTL reduction + rebalance trigger)`
2. `feat(cluster): agent restart/rejoin recovery via NATS supervisor bootstrap path`
3. `test(nix): full-suite 3-node nixosTest (portal bootstrap→apply→reboot→remove→rejoin, ApplyRecord assertions)`
4. `docs: DESIGN.md, AGENTS.md, ADRs (incl. 0004-apply-record-schema, 0005-portal-cluster-bootstrap, demo + two gitops modes)`

---

## M8 — Interactive demo environment (Wave 8)

**Goal:** provide a one-command interactive demo — **not** the automated `nixosTest`
driver — that boots 3 QEMU VMs interactively so the user can click through the portals
end-to-end. `nix run .#demo` builds each node's `system.build.vm`, starts all three
backgrounded with a shared multicast LAN, trap-kills on Ctrl-C, and prints the portal
URLs (http://localhost:8081–8083), SSH ports (2221–2223), and cluster IPs
(192.168.42.1–3). This is the canonical manual QA path that complements the fully
automated `full-suite` check.

**Verified pattern (nixfleet-style; confirmed against nixpkgs `qemu-vm.nix`):**
- Each node = `nixosConfigurations.nodeN.config.system.build.vm` (added in M0). The
  `run-*-vm` script accepts extra QEMU args appended at runtime.
- Per-node port-forwarding via `virtualisation.vmVariant.virtualisation.forwardPorts`:
  host 8081/8082/8083 → guest 8080 (portal); 2221/2222/2223 → guest 22 (SSH for
  debugging).
- Inter-VM LAN: launcher appends to each VM's QEMU line:
  ```
  -netdev socket,id=vlan0,mcast=230.0.0.1:14242,localaddr=127.0.0.1 \
   -device virtio-net-pci,netdev=vlan0,mac=52:54:00:12:34:0<N>
  ```
  Unique MAC per node; multicast sockets are **unprivileged** — no root, no TAP, no VDE.
- That NIC appears as `eth1` with NO DHCP → each node's NixOS config sets a static IP:
  `192.168.42.1/24` (node1), `.2` (node2), `.3` (node3). NATS cluster traffic binds to
  and advertises the eth1 address.
- Launcher = `writeShellApplication` exposed as `apps.x86_64-linux.demo`.

**Rejected alternatives (one-line reasons each — record in ` docs/adr/0006-demo-approach.md`):**
- `nixosTest driverInteractive` → VDE+TAP needs root, REPL-centric, debugging tool
  (not a deliverable UX).
- `nixos-shell` → single-VM only; cannot form a multi-node LAN.
- `microvm.nix` → TAP needs root; user networking is per-VM isolated (no shared LAN).

**Expected files/packages:**
- `nix/vm-common.nix` — NixOS module that: sets the static `eth1` IP (parametrized by
  node index), adds `services.nixmox.config.nats.advertiseAddr = eth1`,
  `services.nixmox.config.http.advertiseAddr = eth1`, and the per-node QEMU
  port-forwarding overrides (via `virtualisation.vmVariant`).
- `nix/demo.nix` — the `apps.x86_64-linux.demo` `writeShellApplication` launcher.
- `docs/adr/0006-demo-approach.md` — the choice + rejected alternatives.
- (No Go code changes — the demo is purely nix + a launcher shell.)

- [ ] **8.1 `vm-common.nix` — static eth1 + NATS/HTTP advertise + QEMU port-forward**

  **What to do:**
  - `nix/vm-common.nix`: a NixOS module taking `{ nodeIndex, portalHostPort,
    sshHostPort }` (per-node overrides).
    ```nix
    # inside the module:
    networking.interfaces.eth1.ipv4.addresses = lib.mkOverride 99 [{
      address = "192.168.42.${toString nodeIndex}";
      prefixLength = 24;
    }];
    # nixmox config rebinds advertise address:
    services.nixmox.config.nats.advertiseAddr = "192.168.42.${toString nodeIndex}";
    services.nixmox.config.http.advertiseAddr = "192.168.42.${toString nodeIndex}";
    # QEMU port-forward only used by the demo, NOT by nixosTest:
    virtualisation.vmVariant.virtualisation.forwardPorts = [
      { from = "host"; host.port = portalHostPort; guest.port = 8080; }
      { from = "host"; host.port = sshHostPort;   guest.port = 22;   }
    ];
    ```
  - Wire `nixosConfigurations.nodeN` (M0) to import `vm-common.nix` with the correct
    per-node indexes/ports — **only** in the demo context (the demo launcher supplies
    the extra QEMU args; the static-IP part is fine in `nixosConfigurations` itself,
    but `forwardPorts` should only be live in `virtualisation.vmVariant`, which is
    enabled by `nix run` and not by the `nixosTest` driver's `toplevel` use). Confirm
    by reading nixpkgs `qemu-vm.nix` that `forwardPorts` under `vmVariant` is what
    `run-*-vm` honors, while `nixosTest` builds from `toplevel` and ignores
    `vmVariant`.

  **Acceptance Criteria:**
  - [ ] `nix build .#nixosConfigurations.node1.config.system.build.vm` builds (the
        demo-able VM); the generated `run-node1-vm` script exists.

- [ ] **8.2 `apps.x86_64-linux.demo` launcher (`writeShellApplication`)**

  **What to do:**
  - `nix/demo.nix`:
    ```nix
    apps.x86_64-linux.demo = let
      vmScript = i: "${self.nixosConfigurations."node${toString i}".config.system.build.vm}/bin/run-node${toString i}-vm";
      qemuLan = i: "-netdev socket,id=vlan0,mcast=230.0.0.1:14242,localaddr=127.0.0.1 -device virtio-net-pci,netdev=vlan0,mac=52:54:00:12:34:0${toString i}";
    in writeShellApplication {
      name = "nixmox-demo";
      runtimeInputs = [ ];
      text = ''
        set -euo pipefail
        pids=()
        cleanup() { kill "''${pids[@]}" 2>/dev/null || true; }
        trap cleanup EXIT INT TERM
        for i in 1 2 3; do
          "$(vmScript "$i")" $(qemuLan "$i") &
          pids+=($!)
          sleep 8   # stagger node boot
        done
        wait
      '';
    };
    ```
  - The launcher prints (after the trap-setup, before `wait`) a banner:

    ```
    nixmox demo cluster ready.
      Portal:   http://localhost:8081 (node1)   http://localhost:8082 (node2)   http://localhost:8083 (node3)
      SSH:      ssh -p 2221 root@localhost      (2222 / 2223 for node2 / node3)
      Cluster:  192.168.42.1  / .2  / .3   (nixmox join target :6222)
      Ctrl-C kills all three VMs.
    ```

  **Acceptance Criteria:**
  - [ ] `nix build .#apps.x86_64-linux.demo` builds (i.e. produces the launcher).
  - [ ] `nix run .#demo` (interactive) boots three VMs, banner appears. AUTO-VERIFIED
        below in 8.3.

- [ ] **8.3 Automated check that the demo boots (`demo-boots` check)**

  **What to do:**
  - Expose a `checks.x86_64-linux.demo-boots` `nixosTest` that **exercises the same
    VM definitions + multicast LAN** but asserts the boot (rather than relying on a
    human to click). This is not the manual demo, but a regression net ensuring the
    VMs come up cleanly:
    ```nix
    # 3 nixosConfigurations.nodeN nodes wired together via the same multicast LAN
    # (the test driver spawns each VM script directly with `qemuLan N` appended).
    nodes.node1.start()
    nodes.node2.start()
    nodes.node3.start()
    node1.wait_until_succeeds("curl -sf http://127.0.0.1:8080/api/v1/bootstrap | grep unclustered")
    node1.succeed("curl -sf -XPOST http://127.0.0.1:8080/api/v1/cluster/create -d '{\"name\":\"demo\"}'")
    node2.succeed("curl -sf -XPOST http://127.0.0.1:8080/api/v1/cluster/join/initiate -d '{\"seed_peer_cluster_url\":\"http://192.168.42.1:8080\"}'")
    node3.succeed("curl -sf -XPOST http://127.0.0.1:8080/api/v1/cluster/join/initiate -d '{\"seed_peer_cluster_url\":\"http://192.168.42.1:8080\"}'")
    node1.wait_until_succeeds("curl -sf http://127.0.0.1:8080/api/v1/hosts | jq -e 'length == 3'")
    ```

  **Acceptance Criteria:**
  - [ ] `nix build .#checks.x86_64-linux.demo-boots` exits 0.

- [ ] **8.4 `docs/adr/0006-demo-approach.md` — chose multicast QEMU over alternatives**

  **What to do:**
  - Record the choice + the rejected alternatives with their one-line reasons
    (`driverInteractive`, `nixos-shell`, `microvm.nix`). Caveats: multicast LAN over
    `230.0.0.1:14242` is best-effort on a single host; do not scale to >5 nodes; this
    is a demo, not a production cluster mesh.

  **Acceptance Criteria:**
  - [ ] `ls docs/adr/0006-demo-approach.md` exists; document is non-empty.

### M8 Manual QA script (the user-facing click-path)

This is the script the user manually executes against `nix run .#demo`. The F3 review
agent (Final Verification Wave) executes it as part of final manual QA, capturing
screenshots at each step.

1. `nix run .#demo` — three VMs boot; banner prints URLs.
2. Open http://localhost:8081 (node1) in a browser. Expected: the portal renders the
   **setup screen** (`setup.html` — unclustered).
3. Click "Create new cluster", enter name `homelab`, submit. Expected: page swaps to
   the cluster view (`portal.html`); host row shows `node1`, online, MemberCount=1.
4. Open http://localhost:8082 (node2). Click "Join existing cluster", enter
   `192.168.42.1:8080` (node1's advertised HTTP address, NOT the localhost-forwarded
   8081 — the inter-VM LAN uses 192.168.42.x). Submit. Expected: portal swaps to
   cluster view; refresh of node1 shows `node2` online; MemberCount=2.
5. Repeat step 4 with http://localhost:8083 (node3). MemberCount=3; R promoted to 3
   (assert via `nixmox status` on node1's SSH: `nix run .#demo` left ssh available at
   `-p 2221 root@localhost`).
6. Back on http://localhost:8081, click "Configure GitOps". Enter the **real git remote
   URL** (this repo's GitHub URL OR a homelab config repo — that's Mode B). Click
   "Pull now". Expected: portal shows repo URL, branch `main`, a real `last_pulled_sha`
   of length 7+ hex, `update_available=false`.
7. Click "Apply" on the `node1` row. Expected: button disables while a job runs; the
   host row's status badge updates to "applying" → "online (genAfter=N+1)"; expand the
   host detail view (`/hosts/node1`) to see the ApplyRecord row (status: success,
   gitSHA, gens, timestamp).
8. Click "Rollback" on `node1`. Expected: portal shows the
   "→ generation {genBefore}" hint; job runs; `genAfter` decrements back.
9. Click "Reboot" on `node2`. Expected: confirm dialog; node2's heartbeat goes offline
   within TTL+slack on node1's view; node2 reboots; heartbeat reappears; latest
   ApplyRecord still visible.
10. Click "Remove" on `node3`. Expected: tombstone appears; MemberCount=2 on node1's
    portal; node3's `applylog.*` keys age out faster (TTL=1d).
11. Ctrl-C in the launching terminal. All three VMs clean up.

### M8 Done-Criteria

```bash
# Automated regression:
nix build .#apps.x86_64-linux.demo                  # launcher builds
nix build .#checks.x86_64-linux.demo-boots          # 3-VM multicast test PASS
ls docs/adr/0006-demo-approach.md                   # exists
# Manual (executed by F3):
nix run .#demo                                      # boots; banner prints URLs
# Then the 11-step click-path above.
```

**Atomic commits:**
1. `feat(nix): vm-common.nix module (eth1 static IP + advertise addrs + vmVariant forwardPorts)`
2. `feat(nix): apps.x86_64-linux.demo writeShellApplication launcher (staggered boot, multicast LAN, trap cleanup, banner)`
3. `test(nix): demo-boots check (3-VM multicast boot + portal bootstrap via 192.168.42.x)`
4. `docs: ADR 0006 demo approach + rejected alternatives`

---

## Final Verification Wave

> Runs after ALL milestone tasks (M0–M8). 4 review agents in parallel; results
> consolidated; user must explicitly "ok" before the plan is closed.

- [ ] **F1. Plan Compliance Audit** — `oracle`
  Read the plan end-to-end. For each "Must Have" in `Design constraints & risks`: verify
  the implementation exists (read file / `go test` / `nix build` / `curl`). For each
  "Must NOT Have": `grep`/`ast_grep_search` codebase for forbidden patterns (R=2 in
  `natsx/kv.go`, RBAC web in `internal/portal`, hot reload loops in `internal/config`,
  cluster-rename logic in `internal/cluster/supervisor.go`). Also verify: ApplyRecord
  schema includes `{timestamp, status, gitSHA, genBefore, genAfter}`; applylog prune
  enforces N=50; portal setup screen appears only when `ClusterState.Status ==
  "unclustered"`; `nixmox join <peer>` CLI and portal Join screen call the same
  internal `JoinCluster` function (grep for both call sites). Check evidence files
  exist in `.sisyphus/evidence/`. Compare deliverables vs plan.
  Output: `Must Have [N/N] | Must NOT Have [N/N] | Tasks [N/N] | VERDICT: APPROVE/REJECT`

- [ ] **F2. Code Quality Review** — `unspecified-high`
  Run `make lint` + `go test ./... -race` + `nix flake check`. Review all changed files
  for: `as any`/`interface{}`, empty catches, `fmt.Println` in prod code paths (debug
  OK in cmd), commented-out code, unused imports. Check AI slop: excessive comments,
  over-abstraction, generic names (`data`/`result`/`temp`/`item`).
  Output: `Build [PASS/FAIL] | Lint [PASS/FAIL] | Tests [N pass/N fail] | Files [N clean/N issues] | VERDICT`

- [ ] **F3. Real Manual QA** — `unspecified-high` (+ `playwright` skill for portal)
  Start from clean `nix flake lock` + empty `/tmp/nixmox-data`. Execute EVERY QA
  scenario from EVERY milestone — follow exact steps, capture evidence (screenshots for
  portal, `curl` bodies for API, `nix build` logs for VM tests). Test cross-milestone
  integration: cluster join → remote action → GitOps apply flow on a 3-node VM suite.
  **Additionally execute the M8 Manual QA script end-to-end** (the 11-step
  click-path): `nix run .#demo`, use Playwright to drive the three portals at
  http://localhost:8081–8083 (create cluster → join node2 → join node3 → configure
  real GitOps URL → apply → rollback → reboot node2 → remove node3 → Ctrl-C). Capture
  one screenshot per step + a final consolidation screenshot of all three portals side
  by side. Save to `.sisyphus/evidence/final-qa/` (including `demo-click-path/`
  subfolder).
  Output: `Scenarios [N/N pass] | Integration [N/N] | Edge Cases [N] | Demo Path [11/11 pass] | VERDICT`

- [ ] **F4. Scope Fidelity Check** — `deep`
  For each milestone task: read "What to do", read actual `git log/diff`. Verify 1:1 —
  everything in spec was built, nothing beyond spec was built. Check "Must NOT do"
  compliance. Detect cross-milestone contamination (e.g. M3 code in M2 commit).
  Verify the v1 out-of-scope list is genuinely absent.
  Output: `Tasks [N/N compliant] | Contamination [CLEAN/N] | Unaccounted [CLEAN/N] | VERDICT`

---

## Commit Strategy (atomic, milestone-squashed)

One squashed commit per milestone, with the per-task atomic commits below it in
history (preserve for review, then squash on merge if PR-style):

| Milestone | Squash commit subject |
|---|---|
| M0 | `chore(skeleton): M0 go module, cobra, config, chi, flake, CI` |
| M1 | `feat(portal): M1 single-node portal (hostname, nixos version, uptime, agent version)` |
| M2 | `feat(jobs): M2 persistence + state machine + goroutine runner + /api/v1/jobs` |
| M3 | `feat(gitops): M3 repo config, cron poll, SHA detection, clone management` |
| M4 | `feat(apply): M4 nixos-rebuild switch job, applied-SHA, rollback, guards, VM test` |
| M5 | `feat(cluster): M5 embedded NATS+JetStream, join, KV buckets, heartbeats, R-scaling` |
| M6 | `feat(actions): M6 remote shutdown/reboot via NATS req-rep, portal buttons, VM test` |
| M7 | `feat(hardening): M7 host removal + tombstone, rejoin recovery, full VM suite, docs` |
| M8 | `feat(demo): M8 interactive demo launcher (nix run .#demo, 3 QEMU VMs, multicast LAN, portal click-path)` |

**Pre-commit hook (each atomic commit):** `go test ./... -race && golangci-lint run`
(fast-fail). The `nix flake check` (VM tests, slow) runs **post-milestone**, not
pre-commit.

---

## Success Criteria

### Verification commands (whole project — copy-pasteable)

```bash
# Lint + unit tests
make lint                                          # exit 0
go test ./... -race                                # all PASS

# Binary smoke
./nixmox version
./nixmox serve &
sleep 1
curl -sf http://127.0.0.1:8080/health              # {"status":"ok",...}
curl -sf http://127.0.0.1:8080/                    # portal HTML, contains "nixmox"
curl -sf http://127.0.0.1:8080/api/v1/jobs         # []
curl -sf http://127.0.0.1:8080/api/v1/hosts        # [this host]
kill -TERM %1   # exits 0 within 2s

# 3-node VM suite
nix build .#checks.x86_64-linux.full-suite        # builds + runs QEMU multi-node test; PASS

# Interactive demo (manual QA path):
nix build .#apps.x86_64-linux.demo                # launcher builds
nix run .#demo                                    # boots 3 VMs; portals on 8081-8083

# ApplyRecord + applylog spot checks (within any running instance):
curl -sf http://127.0.0.1:8080/api/v1/hosts/node1 | jq -e '.apply_record.status'
curl -sf http://127.0.0.1:8080/api/v1/hosts/node1/applylog | jq -e 'length >= 1'

# Cluster hand-checked (inside a nix build of the check)
#  - node1: portal setup → "Create new cluster" homelab
#  - node2: portal → "Join existing cluster"  http://192.168.42.1:8080 → KV shows both hosts online
#  - node3: portal → "Join existing cluster"  → R promoted to 3
#  - portal:GET /api/v1/hosts → [3 hosts, all online, each embedding latest ApplyRecord]
#  - POST /api/v1/hosts/node2/reboot → node2 reboots, KV marks offline, returns online after boot
#  - POST /api/v1/hosts/node3/remove → node3 KV tombstoned, peers rebalance, applylog TTL→1d
```

### Final checklist
- [ ] All "Locked decisions" in the overview are present in code.
- [ ] All v1 out-of-scope items are **absent** from code.
- [ ] `go test ./... -race` passes.
- [ ] `nix build .#checks.x86_64-linux.full-suite` passes (3-node QEMU suite, portal-driven bootstrap).
- [ ] **`nix build .#apps.x86_64-linux.demo` + `nix run .#demo`** boots 3 QEMU VMs; portals reachable on `localhost:8081`/`8082`/`8083`; the 11-step M8 manual QA click-path executed end-to-end.
- [ ] Self-restart guards present in `nix/module.nix`.
- [ ] ApplyRecord schema `{timestamp, status, gitSHA, genBefore, genAfter}` emitted on every apply/rollback; latest embedded in `hosts.<nodeID>`; history in `applylog.<nodeID>.<ts>` (N=50 prune).
- [ ] NATS supervisor supports runtime reconfigure (create/join cluster without daemon restart); cluster name immutable after create.
- [ ] `nixmox join <peer>` CLI and portal Join screen share the same `JoinCluster` code path.
- [ ] Two gitops repo modes addressed (Mode A hermetic fixture in `nixosTest`, Mode B real URL via portal).
- [ ] `DESIGN.md`, `AGENTS.md`, and ADRs finalized (incl. `0004-apply-record-schema`, `0005-portal-cluster-bootstrap`, `0006-demo-approach`).
- [ ] Persistence dir layout matches the "Persistence dir layout" section (incl. `applied.json`).

---

## Open questions (minimal — most items are locked)

1. **Go module path.** The plan assumes `github.com/helios/nixmox`. Confirm by reading
   `README.md`/`LICENSE` in task 0.1 before `go mod init`. (Auto-resolve: if README
   states a different path, use it.)
2. **M4 dry-activate vs real `switch` in the VM test.** Confirmed baseline =
   `dry-activate` (safe). Stretch goal = real `switch` of a trivial config change (e.g.
   add `systemd.services.dummy-apply-test`), gated on whether the VM test can reliably
   observe `genAfter > genBefore`. If infeasible, keep `dry-activate` baseline and add
   a TODO comment. (Default if not resolved: keep baseline.)
3. **Interactive demo cluster IP range.** The demo uses `192.168.42.0/24` for eth1
   static IPs. If a developer's actual LAN uses `192.168.42.0/24`, the demo's
   host-forwarded portals still work (those are `localhost:8081`/`8082`/`8083`), but a
   user could be confused by route collisions when both their homelab and the demo are
   alive. Default: keep `192.168.42.x` (it's a deliberate C-class reserved feel);
   override via a flake input if a user reports a conflict.
4. **`/etc/nixmox-flake-rev` precedence — confirmed.** By Amendment 1 the gitSHA
   resolution precedence is locked: `/etc/nixmox-flake-rev` (when present and non-
   `"unknown"`) → else `mgr.CurrentSHA()`. Recorded here only for the executor; not an
   open question.
---

## Execution Appendix (Momus review, 2026-08-06)

Momus verdict: technically sound; rejected only on missing structural metadata. Closed
here compactly (user directive: no further plan-polishing cycles).

- **Dependency chain:** strictly sequential M0 → M1 → M2 → M3 → M4 → M5 → M6 → M7 → M8.
  Each milestone depends on all prior ones; no cross-milestone parallelism (tasks within
  a milestone may parallelize only when they touch disjoint files, e.g. M1 portal
  templates ∥ hostinfo provider).
- **Delegation defaults:** portal/UI tasks → `visual-engineering` (+`frontend-ui-ux`);
  hard logic (job runner, NATS supervisor) → `deep` or `unspecified-high`; nix checks/
  demo → `unspecified-high`; docs → `writing`. Empty `load_skills` unless a skill domain
  genuinely overlaps.
- **TODO list:** maintained live in the executing session's todo tracker (M0–M8 as
  top-level items, expanded per milestone at execution time) rather than duplicated here.

Status: **final**. Further changes via new amendments only, not review cycles.
