# API and interface contract

All examples define the proposed rewrite API. The CLI, browser, and TUI submit
the same command objects; no interface may bypass validation or task admission.
All three connect to the console over HTTP, including secret submission. Automated
TLS protects NATS only; enrollment/cache access uses HTTP. Cross-node secrets use
Core NATS. See [listeners](runtime-configuration.md#service-ports).

## Values and references

- JSON uses snake_case; timestamps are UTC RFC3339; sizes and log offsets are bytes.
- IDs are opaque generated strings. Names are labels, never transport identities.
- Mutable records carry `version`, an increasing concurrency token. Observed state
  includes freshness metadata; a cached record is not proof that a target is reachable.
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

`ref_id` identifies an upstream or local-change reference. `commit` may be null in
an unresolved editable binding or guided installation before preparation. Builds
are admitted with pinned source; installation pins it before build/erase phases.
Changing the binding does not change tasks or the reported running system.

## Command API

`POST /api/v1/commands` is the normal mutation entry point. The operation registry
validates the typed payload. Reads use resource endpoints. No arbitrary shell
execution endpoint is exposed.
The [browser HTML adapter](#browser-html-and-live-updates) translates forms into
these same command objects and invokes the same admission service.
Secret import has a dedicated redacted ingress described below; it dispatches
only a secret-free reference through this command path.
Rebase uploads stage bytes through the draft resolution ingress; accepting those
files still requires a typed command. Enrollment has its own limited machine
exchange. These exceptions do not expose arbitrary resource mutations.
Membership-status reads return the signed removal evidence defined in
[identity and trust](identity-and-trust.md#authenticated-removal-response) over
plain HTTP. Revoked nodes verify the payload against pinned cluster trust without
restoring NATS access or authenticating the HTTP server.

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
Persist resolved selectors under that receipt before acceptance; a retry must not
resolve a newer commit, release, or builder. Context is node-local for bootstrap
operations, cluster-scoped otherwise.

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

### Operation registry and scope

Feature operation tables define typed payloads. IDs are strings, versions are
positive integers, flags are booleans, and timestamps are RFC3339. Reject unknown
fields. Every entry declares payload validation, scope, target/capabilities,
version preconditions, result resource, and task/reconciliation handlers.
Payload `host_id`, when present, must equal envelope `target_id`; it cannot retarget
a command. `serve`, `tui`, and reads do not submit mutation commands.

| Scope | Operations | Admission |
| --- | --- | --- |
| `node_local` | `network.check`, `network.wifi.scan`, `network.wifi.connect`, `discovery.search`, `identity.reset`, `cluster.join`, `cluster.create` | Persist receipts/tasks on the addressed node using the existing task engine; no cluster quorum required |
| `cluster` | All other feature mutations | Admit through the cluster's durable command stream with quorum |
| Inherited | `task.cancel`, `task.retry` | Use the referenced task's recorded scope and owner |

Scope is selected by the registry, never by caller preference or transport failure.
Reuse the existing receipt/task stores and embedded broker where applicable;
local scope does not require a second journal or queue implementation.
Local operations cannot mutate another node's cluster state. Join approval remains
cluster-scoped; `cluster.join` only coordinates the local request/credential handoff.
`cluster.create` is unjoined-only: persist its new cluster ID once, initialize the
standalone broker, and report success only after durable genesis is committed.
Never use it to replace an unavailable existing cluster. Local receipts remain
queryable on their owner after joining; no automatic replay into cluster commands.

### Reviews and concurrency

`expected_versions` uses `<resource-kind>:<id>` keys, for example `host:n_cedar`,
`repository:r_lab`, `draft:draft_01`, `preparation:prep_01`, or `task:task_01`.
CLI `--version` and `--expected-version` populate the operation's primary key;
they are not duplicate payload fields. Clients copy other required versions from
the displayed draft/plan/review, never silently fetch newer versions to force success.

```json
{
  "review": {
    "id": "review_01", "kind": "installation_erase", "state": "pending",
    "binding": {"host_id": "n_cedar", "preparation_id": "prep_01", "preparation_version": 4,
      "source_commit": "1111111111111111111111111111111111111111", "disk_ids": ["disk_01"]},
    "required_acknowledgements": {"typed_device_paths": ["/dev/disk/by-id/nvme-EXAMPLE"]}
  }
}
```

Reviews live inside the owning task or plan; there is no separate review resource
or collection. Task resumptions submit the task ID, current `review_id`, and
acknowledgements. Plan approvals use the plan ID/version and required acknowledgements.
Atomically check that approval is still applicable, then record it; duplicate
submissions cannot resume effects twice. Replace the task review ID or plan ID
when relevant source, devices, configuration, or intended effects change.
Plan IDs identify immutable reviewed effects; status versions cannot silently
change what a saved `--plan` approves.
Health updates, log/progress updates, and unrelated host metadata
do not invalidate it. Recheck relevant facts before effects; a new observation ID
alone is not a change in those facts. Stale bindings return `CONFIRMATION_STALE`.
There is no universal review timeout: device observations use onboarding's freshness
rules; other reviews remain valid while their bindings hold. Deployment diff
approval retains its existing `diff_id`/`diff_version` contract.

Draft and preparation reads include `expected_versions` for themselves and the
repository version captured with their diff. Commit checks both. Editing/rebasing
invalidates the prior review. A conflict requires a newly rendered review, not a
version-only retry. `expected_versions` protects the records a mutation changes;
do not copy unrelated host/task versions into a confirmation. Reads never renew
or approve reviews; the owning operation handles re-review when bindings change.

## Reads and events

Read collections: `/hosts`, `/clusters`, `/discoveries`, `/repositories`,
`/configurations`, `/drafts`, `/artifacts`, `/preparations`, `/media`, `/candidates`,
`/deployments`, `/diffs`, `/tasks`, `/credentials`, `/observations`, `/publications`,
`/cluster/requests` (cluster-side join approval queue).
Additional collections are `/gc-plans`, `/media/plans`, and `/diagnostics`.
Prefix all with `/api/v1`. Singleton reads are `/settings`, `/identity`,
`/cluster/status`, and `/cache/status`. Cache location lookup and streaming routes
are defined in [cache gateway](cache-gateway.md).
Collections return `{items, next_cursor}`; `next_cursor` is null at the end.
Use `cursor` and `limit` (default 100, range 1–500). Cursors bind filters, ordering,
and position; they do not require retained database snapshots or a fixed lifetime.
Tasks, deployments, publications, and diagnostics sort newest first by `(created_at, id)`;
named catalogs sort by display name then ID; other collections default to ID order
unless their feature specifies otherwise. Live lists may change between pages:
clients deduplicate by ID and refresh for current state. Revision-based catalogs,
draft files, and diffs pin their source/version across pages; changed/unavailable
versions fail explicitly rather than mixing revisions. Invalid cursors/filters or
bounds return 400; an unavailable cursor position returns 410 `CURSOR_EXPIRED`.
Each collection has a `/{id}` detail route using opaque IDs. Static routes such
as `/media/profiles` take precedence.
Resource details have stable IDs; mutable records have versions. Observations and
live status include `observed_at` and `freshness` (`live`, `stale`, `unknown`);
immutable source/output records use their commit/digest instead. Owner-local
resources identify `owner_node_id`. Unavailable owner data is 503,
missing IDs are 404, and known expired/deleted download payloads are 410.

| Collection | Supported filters / projection |
| --- | --- |
| `/hosts` | Filters `membership`, `reachability`; host record includes health and configuration |
| `/clusters` | `name`, `gateway_endpoints`, `membership`, `health` |
| `/discoveries` | Filter `kind=node\|cluster`; `kind`, `node_id`/`cluster_id`, `name`, `endpoints`, `key_fingerprint`, `expires_at`; inapplicable fields are null |
| `/repositories` | Repository remote/upstream record |
| `/configurations` | Filters `repository_id`, `ref_id`, `commit`; revision selection follows the configuration catalog contract |
| `/drafts`, `/artifacts`, `/gc-plans`, `/publications` | Filter `repository_id`; publications include `repository_id`, `ref_id`, `commit`, `remote_branch`, nullable `pull_request_url`, `remote_head`, `integration_base`, `state` |
| `/preparations`, `/observations`, `/deployments`, `/diffs`, `/diagnostics` | Filter `host_id`; corresponding feature record |
| `/candidates` | Filters `repository_id`, `configuration`; pinned candidate record |
| `/media` | Filter `profile_id`; verified image record |
| `/media/plans` | Filter `writer_id`; image/device/review binding |
| `/tasks` | Filters `host_id` (target), `kind`, `state`, `since`, `until`; UTC creation-time interval `[since, until)` |
| `/credentials` | Filter `kind`; metadata only |
| `/cluster/requests` | Filter `state`; request identity, key fingerprint, expiry, and decision |

Nested collections use the same pagination bounds unless explicitly overridden.
Settings return `{version, values, ownership}` with secret values redacted;
identity and network/cache status use their feature schemas. Cluster status includes
`observed_at`, `freshness`, `cluster_id`, `membership`,
`health`, and `quorum: {available, replicas, required}`; unjoined cluster_id is null.

`GET /api/v1/events?topics=hosts,tasks,repositories` opens one SSE stream per client.
Send a snapshot first, then versioned invalidations/updates. On reconnect send a
fresh snapshot; do not promise durable replay of every UI event.
`topics` is a comma-separated optional selector; omission subscribes to all
resource topics available in this scope. Topics are collection paths with the
leading slash removed and remaining `/` replaced by `.`, plus `settings`, `identity`,
`cluster.status`, `cache.status`, `media.profiles`, `network.status`, and
`network.wifi.networks`. `tasks` includes progress/activity
updates; log bytes require explicit `task.logs` plus `task_id=<id>`. Unknown topics
and log selectors without `task.logs` return 400. Optional `activity_id` restricts
logs to that task's activity; log events then include `activity_id` and offsets
within that activity's log. Bootstrap clients receive local snapshots only. Snapshot contents use
bounded first pages plus collection links; never embed unbounded histories.

`hosts.telemetry` is an opt-in topic requiring `host_id=<id>`; omission of `topics`
does not subscribe to it. Reject a telemetry selector without that topic. It sends
`host.telemetry` events containing the [latest telemetry sample](host-inventory-and-lifecycle.md#live-host-telemetry),
starting with a snapshot and refreshing it on reconnect. Sample stream/sequence
numbers are independent of host record versions. Unjoined access is local-only.

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

### Browser HTML and live updates

| Route | Contract |
| --- | --- |
| `GET /ui/...` | Registered console views: full HTML on navigation, fragments for htmx requests; same read services and scope as JSON API |
| `POST /ui/commands` | Typed form adapter to the command service; HTML receipt/review/errors with the same command/task IDs and status meanings |
| `GET /api/v1/events` | Shared JSON SSE stream for live browser updates; unchanged API event schema |

Vary full-page/fragment responses by `HX-Request`. Keep `/api/v1` responses in their
documented formats; never return HTML fragments to JSON clients. HTML forms retain
command IDs across retries and submit the reviewed expected versions/acknowledgements.
They cannot bypass admission, change scope, or execute effects directly. Secret
fields still use the dedicated redacted ingress; no secret enters `/ui/commands`.

Use one SSE connection per browser tab, selecting visible resource topics and
explicitly watched logs. A small bridge coalesces versioned events into htmx fragment
refreshes; discard stale events/responses and preserve active form values. Render
log chunks as text using the existing offset/gap recovery contract. On reconnect,
refresh snapshots and affected fragments; show stale/disconnected state meanwhile.
HTML is a presentation of the shared data, not a second authoritative state model.

## CLI contract

```sh
nixmox --endpoint http://alder:63170 hosts list --json
nixmox --endpoint http://alder:63170 tui
nixmox apply
nixmox apply cedar --yes
nixmox tasks show task_01 --json
nixmox tasks logs task_01 --follow
```

Apply aliases and source/builder defaults follow [Apply latest](deployment-and-rollback.md#apply-latest).
The CLI resolves the local node and binding into the command envelope; the API
never guesses a target from the serving gateway.

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

All interfaces use the same feature flows. Additional CLI verbs must declare their
command type and payload schema in the operation registry.

## Secret ingress

`POST /api/v1/credentials/import` accepts credential metadata, storage node ID,
and a sensitive value stream over the same HTTP connection as other console calls.
Remote delivery uses the [Core NATS secret path](identity-and-trust.md#secret-delivery-over-nats),
not HTTP forwarding. The holder privately stages the value, then submits
`credential.import` through JetStream with only `staging_ref`. Same-node imports
use that handler locally. Return the normal receipt; after a timeout, query it
before retrying the same ID. Erase expired unclaimed staging, not accepted work.
Never log/trace secret bodies or include them in ordinary commands. The initial
user-to-console HTTP hop remains unencrypted.
