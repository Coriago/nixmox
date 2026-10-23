# Current chunk: GitOps, deployment/rollback and distributed cache

Basic cluster management is complete. This chunk manages existing installed NixOS
hosts with running agents and existing remote-backed flake configurations.

## Work

- **GitOps:** repository import/fetch, configuration selection and host bindings,
  pinned source, isolated worktrees, incremental JetStream artifacts, versioned
  refs, rebase/conflict resolution, optional publication/merge, retention and GC.
- **Distributed cache:** local `/cache/` aggregation and management-network
  `/store-cache/` serving; signed narinfo/NARs, peer discovery, bounded HTTP streaming,
  GC-rooted exports, credential selection, status/locate and local-build fallback.
- **Deployment/rollback:** preview/build, candidates, structured closure diffs,
  approval/resumption, Apply in `switch` or `boot` mode, observed running/boot state,
  generation history, retained-output rollback and interrupted-task reconciliation.
- Wire these through the CLI/API, including required reads, reviews, progress and
  task/log watching. Production browser/TUI integration remains separate.

Contracts: `docs/gitops.md`, `docs/cache-gateway.md`, and the ordinary deployment
and rollback sections of `docs/deployment-and-rollback.md`. Reuse the shared API,
task, runtime and identity contracts rather than duplicating their rules here.

**Excluded:** fresh installation, installer images/media, installer entry/kexec,
bootstrap, onboarding, configuration generation and disk formatting. Consume existing
configurations; rebase conflict resolution is still in scope. No multi-host rollouts
or automatic rollback. Ask before expanding scope for a prerequisite.

## Where things go

Keep **CLI → HTTP API → services → engines**.

| Location | Responsibility |
| --- | --- |
| `internal/domain/` | Repository/ref/artifact, candidate/diff/deployment, generation and cache models |
| `internal/service/` | GitOps, cache and deployment orchestration; source resolution, bindings, approvals, retention and reconciliation |
| `internal/engine/git/` | Remote operations, isolated worktrees, incremental bundles and publication primitives |
| `internal/engine/nix/` | Evaluation/build/activation plans, closure diffs, store inspection and NAR export |
| `internal/engine/task/` | Durable execution, exclusions, checkpoints, recovery, logs and progress adapters |
| `internal/engine/comms/` | JetStream artifacts/metadata and authenticated peer messaging; never Nix closure bytes |
| `internal/engine/identity/` | Existing identity/trust and credential primitives needed by these flows |
| `internal/gateway/` | HTTP commands/reads/events, cache routes and bounded streaming |
| `internal/client/`, `internal/cli/` | Typed API calls, commands, review prompts, output and watches |
| `internal/runtime/`, `nix/module.nix` | Shared configuration, cache credentials/substituters and NixOS integration |
| `internal/app/` | Composition and lifecycle only |

Reuse existing engine contracts, remote routing, receipts and observability. Keep
feature policy in services and transport/storage mechanics in engines; no parallel
execution paths or new generic framework.

## Done when

The CLI/API can import and materialize pinned source across nodes, rebase/publish
changes, and protect retained artifacts; then build → substitute or compile locally
→ diff → approve → apply → observe → roll back. Boot-only mode reports staged,
not running. Missing source/output, stale approval, cache failure and interrupted
effects must fail explicitly or reconcile without silently changing the candidate.
