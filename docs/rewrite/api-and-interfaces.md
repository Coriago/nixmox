# API and interface contract

All examples define the proposed rewrite API. The CLI, browser, and TUI submit
the same command objects; no interface may bypass validation or task admission.

## Values and references

- JSON uses snake_case; timestamps are UTC RFC3339; sizes and log offsets are bytes.
- IDs are opaque generated strings. Names are labels, never transport identities.
- Mutable records carry `version`, an increasing concurrency token. Reads include
  freshness metadata; a cached record is not proof that a target is reachable.
- A configuration is repository plus named configuration. Repository metadata maps
  that name to a flake directory and attribute. Execution always pins a commit.

```json
{
  "repository_id": "r_lab",
  "configuration": "cedar",
  "ref_id": "ref_cedar",
  "commit": "1111111111111111111111111111111111111111",
  "flake_directory": ".",
  "flake_attribute": "nixosConfigurations.cedar"
}
```

`ref_id` identifies an upstream or local-change reference. `commit` is null in an
unresolved editable binding, but never null in an admitted build/install task.
Changing the binding does not change tasks or the reported running system.

## Command API

`POST /api/v1/commands` is the normal mutation entry point. The operation registry
validates the typed payload. Reads use resource endpoints. No arbitrary shell
execution endpoint is exposed.
Secret import has a dedicated protected ingress described below; it dispatches
only a secret-free reference through this command path.
Membership-status reads use the narrow authenticated-server exception defined in
[identity and trust](identity-and-trust.md#authenticated-removal-response), allowing
revoked nodes to confirm removal without restoring membership access.

```json
{
  "id": "cmd_apply_cedar_01",
  "type": "deployment.apply",
  "target_id": "n_cedar",
  "expected_versions": {"host:n_cedar": 7},
  "payload": {"candidate_id": "cand_01", "mode": "switch", "auto_approve": false}
}
```

Client-generated command IDs are stable across transport retries. The service
adds authenticated cluster context, admission time, expiry, and a canonical
request hash; the caller cannot override these. Same ID/same request returns the
same receipt. Same ID/different request returns `IDEMPOTENCY_CONFLICT`.

```json
{
  "command_id": "cmd_apply_cedar_01",
  "admission": "accepted",
  "task_id": "task_01",
  "resource_id": null
}
```

Acceptance is HTTP 202, not deployment success. Synchronous validation errors are
400/422; version conflicts are 409; unavailable quorum/capability is 503. A timeout
after submission has an uncertain outcome: query
`GET /api/v1/commands/{command_id}` before resubmitting the same ID.

```json
{
  "error": {
    "code": "VERSION_CONFLICT",
    "message": "Configuration changed after this review.",
    "retryable": false,
    "details": {"resource_id": "cfg_cedar", "expected": 7, "actual": 8}
  },
  "command_id": "cmd_apply_cedar_01"
}
```

Error codes also include `QUORUM_UNAVAILABLE`, `TARGET_UNREACHABLE`,
`SOURCE_UNAVAILABLE`, `ARTIFACT_UNAVAILABLE`, `IDENTITY_CONFLICT`, `CONFIRMATION_STALE`,
`CREDENTIAL_UNAVAILABLE`, `REBASE_CONFLICT`, and `UNSUPPORTED_CAPABILITY`.
`INSTALLATION_UNSUPPORTED` means a running-OS target has no supported path into
the installer; include method-specific reasons and do not start a fallback task.
The UI uses codes for actions and messages for explanation; it never parses logs
as the authoritative status API.

## Reads and events

Read collections: `/hosts`, `/clusters`, `/discoveries`, `/repositories`,
`/configurations`, `/drafts`, `/artifacts`, `/preparations`, `/media`, `/candidates`,
`/deployments`, `/diffs`, `/tasks`, `/credentials`, `/observations`, `/publications`,
`/cluster/requests` (cluster-side join approval queue).
Prefix all with `/api/v1`. Singleton reads are `/settings`, `/identity`,
`/cluster/status`, and `/cache/status`. Cache location lookup and streaming routes
are defined in [cache gateway](cache-gateway.md).
Collections return `{items, next_cursor}`; `next_cursor` is null at the end.
Detail routes use opaque IDs. Operation-specific nested reads appear in feature docs.

`GET /api/v1/events?topics=hosts,tasks,repositories` opens one SSE stream per client.
Send a snapshot first, then versioned invalidations/updates. On reconnect send a
fresh snapshot; do not promise durable replay of every UI event.

```json
{
  "type": "resource.updated",
  "resource": "host",
  "id": "n_cedar",
  "version": 8,
  "observed_at": "2026-10-20T12:00:00Z"
}
```

A separate `task.log` event carries `{task_id, offset, data_base64}`. Decode byte
chunks incrementally, ignore duplicates, fetch gaps, and render as text. Logs and
snapshots have bounds; clients display stale/disconnected state on stream loss.

`task.progress` events carry `{task_id, version, observed_at, progress}` using the
structured deployment progress schema. Activity updates identify activity ID and
attempt. Reconnect fetches the latest `/tasks/{id}/progress` and activity snapshot;
ignore older versions and refresh after gaps. Do not derive counters by counting
received UI events. Browser, TUI, and CLI use the same progress data and formatting
rules, including explicit unknown/stale values.

Package diffs are persisted structured resources with paginated changes and a
complete summary. Emit `resource.updated` for diff completion/invalidation and
refresh its linked candidate review. Do not transport terminal-rendered diffs as
the authoritative data model.

## CLI contract

```sh
nixmox --endpoint http://alder:63170 hosts list --json
nixmox --endpoint http://alder:63170 tui
nixmox apply
nixmox apply cedar --yes
nixmox tasks show task_01 --json
nixmox tasks logs task_01 --follow
```

`apply [host]` aliases `deploy apply [host]`. Both default to the local node's
assigned configuration, latest tracked revision, automatic builder selection,
`switch`, and foreground progress with diff approval. Only explicit overrides
need flags; see [apply defaults](deployment-and-rollback.md#apply-latest).
The CLI resolves the local node ID and reads its binding/version to populate the
normal command envelope; the API never guesses a target from the serving gateway.

Mutations print command/task IDs. `--wait` waits for terminal task status or a
required approval that cannot be prompted for in a noninteractive client;
`--json` emits the same resource representation as the API. Apply waits by default;
`--detach` opts out, and combining `--detach` with `--wait` is invalid. Other
mutations return on acceptance unless `--wait` is supplied. In detached execution,
exit 0 means accepted only. When waiting, exit 0 means succeeded, 1 means failed
or cancelled, 2 means invalid input, 3 means unavailable/unknown outcome, and
4 means awaiting approval (task remains resumable).
`--command-id` permits caller-controlled retry identity. Interactive clients save
pending IDs locally before sending. `apply --yes` sets `auto_approve: true`
for that deployment's diff review. Without it, interactive foreground execution
prompts at review; scripts can later use `deploy approve` with the displayed
diff/version. `--json` never prompts; without `--yes`, waiting returns exit 4
when approval is required. Detached tasks also pause unless auto-approved.
Installation, disk erasure, and media writes retain their specific confirmation
requirements; deployment auto-approval does not cover those operations.
`deploy install --wait` and installer-entry reviews use the same exit 4 behavior
when user input is required. Boot approval and erase confirmation are separate;
`deployment.approve` applies only to candidate deployment, not destructive install.

`deploy install <host>` is the only public installation verb, for both initial and
fresh installs. `--preparation` optionally supplies an existing committed preparation;
otherwise its task guides preparation. Onboarding verbs only inspect/prepare/commit.
Browser/TUI use Apply for updates and Install for fresh installation, backed by the
same `deployment.apply` and `deployment.install` commands as the CLI.

## TUI and browser conventions

Both expose Hosts, Configurations, Repositories, Tasks, Media, and Settings.
Unjoined nodes open Clusters for discovery/join/create; installer mode first shows
network setup. Browser and TUI share these flows and reflect joins initiated from
another console. Local bootstrap services run before enrollment and remain
available without cluster quorum; they cannot mutate another node's cluster state.
TUI: Tab/Shift-Tab move focus, arrows select, Enter opens/activates, Esc returns,
`?` shows actions. Browser: the same named screens/actions use links and buttons.
Actions open their options/review panel, then submit the same command. Apply latest
is available on host-list rows and detail pages; its Auto-approve toggle defaults
off. Build and diff review happen within the resulting task panel, with approval
required only when auto-approval is off. Each task opens
progress, logs, result, and eligible retry/cancel actions. Closing either UI does
not cancel work. Selection state is local; task state belongs to the service.

Every feature's interaction table is a parity checklist. Additional CLI verbs
must declare their command type and payload schema in the operation registry.

## Protected secret ingress

`POST /api/v1/credentials/import` accepts credential metadata, storage node ID,
and a sensitive value stream over local/protected transport. It durably stages
the value in the selected replica's private credential store and submits
`credential.import` containing only that staging reference. Return the normal
command receipt; retain the same request ID across uncertain retries. Expire and
erase unclaimed staged values. Redact request bodies at this endpoint before any
logging/tracing middleware. Ordinary commands reject raw secret fields.
