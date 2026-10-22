# Distributed GitOps

GitOps owns remote-backed repositories, local changes, and optional publication.
It accepts reviewed files from configuration generation and onboarding; it does
not collect hardware, select disks, or decide installation success.

Every repository has a remote base. Each node pulls that base independently.
Nixmox propagates only local changes through JetStream, which handles their durable
storage and replication. There is no full-repository replication service or
local-only repository initialization.

## Repository and reference objects

```json
{
  "schema_version": 1,
  "id": "r_lab",
  "version": 12,
  "name": "lab",
  "remote": {"url": "https://github.com/example/nixos-config.git", "credential_ref": null},
  "upstream": {"branch": "main", "commit": "0000000000000000000000000000000000000001", "fetched_at": "2026-10-20T12:00:00Z"}
}
```

`remote.url` is required; credential_ref and fetched commit/time may be null.
Each repository has isolated refs,
objects, credential references, and configuration names. Registering `services`
creates a distinct repository even if it has a configuration also named `cedar`.

```json
{
  "id": "ref_cedar",
  "repository_id": "r_lab",
  "kind": "change",
  "name": "change/new-cedar",
  "head": "1111111111111111111111111111111111111111",
  "upstream_ref_id": "ref_main",
  "ahead": 1,
  "behind": 0,
  "publication": {"state": "local", "remote_branch": null, "pull_request_url": null}
}
```

Reference kind is `upstream` or `change`. Hosts with configurations already upstream
bind directly to the upstream ref. A change ref is created only for unpublished
changes, including changes to an upstream configuration. It can serve one or more
configurations and need not have a host. All edits occur in isolated worktrees.

## Artifact object and storage

```json
{
  "schema_version": 1,
  "id": "art_01",
  "version": 1,
  "repository_id": "r_lab",
  "command_id": "cmd_commit_01",
  "commit": "1111111111111111111111111111111111111111",
  "remote_base_commit": "0000000000000000000000000000000000000001",
  "prerequisites": ["0000000000000000000000000000000000000001"],
  "bundle": {"object_key": "r_lab/art_01", "sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "bytes": 8192},
  "storage": {"state": "stored"},
  "retention": {"state": "unpublished", "published_commit": null, "pins": ["task_build_01"]},
  "validation_id": "validation_01",
  "task_ids": ["task_build_01"],
  "publication_id": null
}
```

Manifest identity/contents are immutable; storage, retention, and linked results are
versioned projections. Artifact means Git source, not a Nix system output.

| Data | Storage and transfer |
| --- | --- |
| Remote base | Each node independently clones/fetches the pinned base from the remote |
| Local Git changes | Incremental bundle in JetStream Object Store, chunked and checksummed |
| Accepted refs and manifests | Versioned JetStream metadata |
| Materialized source | Rebuildable node-local checkout: remote base plus retained increments |
| Worktrees/drafts | Private owner-local storage; not claimed replicated |
| Nix closures | Ordinary signed HTTP cache substitution during builds; outside JetStream and Git artifacts |

Bundles preserve commit IDs, modes, binary files, and ancestry. A receiver verifies
prerequisite commits and imported objects before building. A node fetches missing
base objects from the remote, then imports required local-change artifacts in
dependency order. Full repository snapshots/base bundles never enter JetStream,
and nodes do not depend on another node serving the base.
[Git bundle contract](https://git-scm.com/docs/git-bundle/2.36.0.html)

JetStream owns replication of artifacts and metadata; configure durability at the
cluster storage layer, not through Git replica counts or placement. Nixmox verifies
the exact checkout needed by each task. A JetStream storage acknowledgement does
not mean every node has already materialized it.

The remote is the recovery source for the base. If it is unavailable, a node may
use its cached pinned base; a node lacking it reports `SOURCE_UNAVAILABLE` and
waits. If remote history no longer supplies a required base, report that explicitly;
never substitute latest upstream or upload a full base into JetStream as fallback.

## Accepting an edit

```text
expected repository version 12
  → render and validate in worktree
  → persist Git objects and incremental bundle
  → await successful JetStream artifact storage
  → CAS repository refs record 12 → 13
  → materialize local refs and notify watchers
```

Use one authoritative refs record per repository for v1, including upstream heads.
This permits atomic checking of branch and upstream versions without inventing a
multi-key transaction. Artifact uploads precede that compare-and-set. Losing a
race leaves an unaccepted recoverable artifact; it never overwrites the winner.
A restart reconciles by command/artifact ID. Quorum loss blocks ref mutation.
Read-only cached source remains available; admission of new work still follows
cluster quorum rules.

Nodes watch accepted refs and materialize changes on demand; acceptance does not
wait for a chosen number of checkouts.

## CLI and interface flows

```sh
nixmox repositories import lab --url https://github.com/example/nixos-config.git --upstream main --wait
nixmox repositories import services --url https://github.com/example/services.git --upstream master --wait
nixmox repositories list
nixmox repositories refs lab
nixmox artifacts show art_01 --json
nixmox repositories fetch lab --wait
nixmox repositories rebase lab --ref ref_cedar --onto ref_main --wait
nixmox repositories publish lab --ref ref_cedar --pull-request --wait
```

Public remotes need no read credentials; private remotes require app-managed read
access for each fetching node. Write credentials are needed only for publication.
Remote failures do not disable local work whose pinned base is cached or operations
on unrelated repositories.

Browser/TUI: Repositories → Add/Import; repository detail → Changes/Refs →
Diff, Rebase, or Publish. Artifact detail shows storage and retention.

Repository list shows remote URL, upstream freshness, and pending refs.
Detail shows ahead/behind counts with fetch time, not an assertion
that the remote still has that head. Artifact detail shows exact commit, changed
files, JetStream storage status, retention/pins, validation, build/install evidence,
and publication. Build readiness reports missing remote prerequisites separately.

## Operation and read API

| Command type | Payload |
| --- | --- |
| repository.import | name, remote_url, upstream_branch, optional credential_ref |
| repository.fetch | repository_id |
| repository.remote.set | repository_id, url, optional upstream_branch (preserve existing); expected repository version |
| repository.credential.set | repository_id, credential_ref |
| repository.rebase | repository_id, ref_id, onto_ref_id; expected repository version |
| repository.rebase.resolve | task_id, resolved_files_ref; expected draft/repository versions from conflict review |
| repository.rebase.abort | task_id |
| artifact.delete | artifact_id; optional plan_id (approval), task_id (resume); expected plan/artifact versions when approving |
| repository.gc.plan | repository_id, optional artifact_ids (default all eligible); produces a plan without deleting |
| repository.gc | repository_id, plan_id; expected plan/artifact versions; recheck affected refs and dependencies |
| repository.publish | repository_id, ref_id, create_pull_request (default false); expected repository version |
| publication.merge | publication_id, expected_remote_head; expected publication version |

Read `/repositories/{id}/refs`, `/repositories/{id}/diff?from=...&to=...`,
`/artifacts/{id}`, `/publications/{id}`. Rebase conflicts expose a retained draft
and file paths; `nixmox repositories rebase-resolve TASK --files DIR --expected-version VERSION` or
`rebase-abort TASK` mirrors the conflict panel in both UIs. No silent conflict choice.

`repositories remote set <repository> --url <url>` preserves the upstream branch;
optional `--upstream <branch>` changes it explicitly. Reads expose the current
repository version used by the mutation envelope. `publications merge <publication>
--expected-head <commit>` submits `publication.merge`; the service checks both the
reviewed remote head and integration base before invoking the provider merge.
Browser/TUI expose the same publication review and Merge action.

Rebase task results expose `draft_id` and the expected draft/repository versions.
Read conflicts through the common draft/file routes. The CLI uploads the reviewed
directory to `POST /api/v1/drafts/{id}/resolutions?version=<version>` as streaming
multipart parts keyed by safe relative path, then submits the returned
`resolved_files_ref`. Uploads only stage owner-local files; they do not edit the
accepted draft/ref. Validate paths and digest manifests, reject symlink escapes,
bind the reference to draft/version, and expire unclaimed staging after one hour.
Resubmitting a command reuses its staged reference; consumed references remain
bound to the receipt. Draft unavailability is an error, never an empty resolution.

## Rebase publication and merge

Rebase against a pinned upstream commit in an isolated worktree. Conflicts leave
the accepted head unchanged. A successful rebase validates and replaces it using
expected repository version. Existing tasks keep old commits. New commit IDs need
new evidence; old results remain historical.

Publication pushes a selected commit to a change branch. Reconcile uncertain push
and PR creation before retrying. Adding credentials never automatically publishes.
For a rewritten published ref, create a replacement remote branch by default;
no automatic force-push. Merge is explicit and subject to provider branch rules.

Before merge, verify exact candidate evidence and integration base. A changed base
requires validation of the integrated result; do not label an untested merge result
as the system already installed. After verified inclusion upstream, bindings may
return to upstream tracking and the local change ref can retire. Squash merges
require content/evidence reconciliation rather than ancestry alone. No automatic
deployment occurs during this transition.

## Change retention and garbage collection

Keep local-change artifacts in JetStream until their changes are verified in the
official upstream branch, or an administrator explicitly deletes/garbage-collects
them. Pushing a PR branch alone does not make a change part of official upstream.
No age-based expiry or queue limit may silently discard unpublished changes;
storage exhaustion rejects new writes with an actionable error.

After verified upstream inclusion, mark artifacts eligible for cleanup. First
check active tasks, rollback/history source pins, and later increments that depend
on them. Squash/rebase merges may publish equivalent content without preserving
the original commit ID; preserve artifacts still needed for that exact commit.
Dependent changes must be safely rebased/repackaged before removing prerequisites.

`nixmox repositories gc lab --dry-run --wait` submits `repository.gc.plan` and
returns a plan listing artifacts, reclaimed bytes, protected references, and
unpublished changes. Apply it with `nixmox repositories gc lab --plan gc_01 --wait`.
Supplying the plan ID explicitly approves its displayed deletion set, including
unpublished changes; live-task dependencies still block it. Browser/TUI: lab →
Retained changes → Clean up → review plan → Confirm.

Interactive single-artifact deletion plans, displays the affected references, and
asks for confirmation in one invocation:

```sh
nixmox artifacts delete art_01 --wait
```

Without a supplied plan, `artifact.delete` pauses its task with a generated
`plan_id`. Interactive `--wait` guides review and resumes that task; noninteractive
waiting returns exit 4. Scripts can resume with `--task <task> --plan <plan>`.
For review in advance, `--dry-run` uses `repository.gc.plan` for the single artifact;
later `artifacts delete <artifact> --plan <plan>` explicitly approves it. The CLI
handles planning internally; users do not have to run a separate planning command.

`GET /api/v1/gc-plans/{id}` returns
`{id, version, repository_id, artifact_ids, reclaimed_bytes, protected_references,
unpublished_artifact_ids, review, expected_versions}`. The embedded review describes
the exact deletion set and affected references under the [shared review rules](api-and-interfaces.md#reviews-and-concurrency).
Recheck dependency/source pins under repository exclusion. Recovery follows the
[task contract](task-engine.md#admission-and-execution); `--wait` reads its plan/result.

Retain small audit records after payload deletion, including publication evidence
or deletion reason. Retire affected change refs explicitly. Never display a deleted
unpublished revision as rebuildable. Node-local Git cache GC is separate and must
not remove objects pinned by running tasks.
