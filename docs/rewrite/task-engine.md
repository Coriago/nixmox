# Task engine

All long operations use a persisted task with generic states and operation-specific
phases. The service, not the requesting UI connection, owns execution lifetime.

## Task object

```json
{
  "schema_version": 1,
  "id": "task_01",
  "version": 6,
  "command_id": "cmd_apply_cedar_01",
  "kind": "deployment.apply",
  "state": "running",
  "phase": "transferring",
  "coordinator_id": "n_alder",
  "builder_id": "n_birch",
  "target_id": "n_cedar",
  "parent_id": null,
  "source": {
    "repository_id": "r_lab", "configuration": "cedar",
    "commit": "1111111111111111111111111111111111111111"
  },
  "progress": {"completed": 1048576, "total": 4194304, "unit": "bytes"},
  "cancellation": {"allowed": true, "requested": false},
  "effects": {"candidate_transferred": false, "activation_started": false},
  "log": {"node_id": "n_alder", "next_offset": 8192},
  "result": null,
  "error": null
}
```

Builder/source are nullable for operations such as shutdown. Unknown total progress
is null, never a fabricated percentage. Results contain resource IDs and observed
outcomes, not success inferred from a shell exit code alone.

`progress` is the compact phase summary, not the full activity model. Build and
deployment tasks additionally expose the structured
[progress snapshot and activities](deployment-and-rollback.md#required-build-and-deployment-progress).
Track activity IDs, attempts, executor, source/cache, counts, bytes, and timings.
Persist the latest snapshot and activity lifecycle outcomes; high-frequency rate
samples may be coalesced rather than fsynced individually. Final activity/result
summaries remain available after task completion.

Normalize progress on the executing node and stream it through NATS to the serving
gateway. Deduplicate by task/activity/attempt and sequence/version. Aggregate child
tasks without counting the same download/build twice. Recovery starts from the
persisted snapshot and reconciles active work; neither restart nor retransmission
may inflate counters or invent completion. Log bytes remain a separate stream.

Build substitution and output transfer use the [cache gateway](cache-gateway.md).
Pin required source outputs and report actual serving peers, bytes, and retries.
Transfer retry preserves candidate identity and never repeats activation or disk
effects. Cancellation releases requests and task pins after safe reconciliation.

## State machine

```text
queued → running → succeeded
   │        ├──→ failed
   │        ├──→ blocked → running
   │        ├──→ reconciling → running / succeeded / failed / blocked
   └────────┴──→ cancelling → cancelled
```

`blocked` means a recorded prerequisite requires action, not terminal failure.
Deployment review uses `blocked`, phase `review`, and reason `APPROVAL_REQUIRED`.
Persist approval policy and evidence, for example:

```json
{
  "auto_approve": false,
  "state": "pending",
  "diff_id": "diff_01",
  "diff_version": 1,
  "candidate_id": "cand_01",
  "target_id": "n_cedar",
  "mode": "switch"
}
```

Approval state is pending/approved/stale; unrelated tasks have no approval object.
Record whether approval was manual or automatic. Duplicate approval commands must
not resume work twice. A restart preserves the pause or recorded approval and
revalidates its evidence before target effects; closing a UI cannot approve work.

`reconciling` means effects may have occurred and must be inspected. Terminal states
are immutable; Retry creates a new linked task. Cancellation can be refused once
an operation reaches a non-interruptible phase; show that reason in every UI.

| Kind | Example phases | Resource exclusion | Restart policy |
| --- | --- | --- | --- |
| repository.fetch | fetch, verify, import | repository ref update | Retry idempotent fetch; CAS metadata |
| configuration.commit | render, validate, store artifact, accept | repository mutation | Reconcile JetStream artifact and accepted head |
| deployment.build | resolve, build, register | builder capacity | Inspect output; rebuild if no verified output |
| deployment.diff | observe baseline, compare, record | bounded metadata reads | Recompute from pinned output metadata |
| deployment.apply | resolve, validate, build, diff, review, transfer, activate, observe | builder capacity during build; target system after review | Preserve approval; inspect running/profile state before retry |
| deployment.rollback | select, activate, observe | target system | Same as apply |
| host.enter_installer | preflight, stage handoff, arm boot/kexec, observe installer | target boot/power and membership transition | Inspect handoff/boot receipt; never repeat an uncertain transition |
| deployment.install | preflight, enter installer if needed, prepare, review, build/transfer, partition, install, reboot, observe | target disks/system; coordinate child exclusions | Resume persisted phases/children; fresh confirmation for destructive retry |
| host.bootstrap | verify SSH target, stage agent, start, observe enrollment | SSH target/bootstrap identity | Reconcile temporary service and receipt; never duplicate identity |
| media.write | verify, write, readback | writing node + device identity | Never resume a partial write automatically |
| host.shutdown | schedule, observe | target power | Inspect receipt; do not reschedule an uncertain action |
| host.refresh / host.diagnostics | collect | bounded read capacity | Safe retry |

Operations register typed payload validation, required capabilities, resource keys,
phase handlers, effect checkpoints, cancellation rules, and reconciliation handlers.
Subprocesses receive argv arrays; no interpolated shell commands from API payloads.

## Admission and execution

1. Consume durable command, validate expected versions, persist an admission intent.
2. Allocate a deterministic task ID from the command ID and persist queued state.
3. Complete command receipt with task ID, then acknowledge command delivery.
4. Acquire resource exclusions and start eligible work. Persist phase intent before
   effects and observed completion afterward.
5. Publish state events and persist final result. Release exclusions only after
   work has stopped or ownership has been safely reconciled.

A restart between steps 1–3 reconciles the same task ID, not a second task. This is
not a claim of exactly-once external effects. The executing node is authoritative
for its local effects; cluster snapshots are a view of persisted execution records.

Proposal: observers/coordinators can be replaced, but another worker must not repeat
an uncertain destructive phase merely because a lease expired. Target-local fencing
and receipts prevent overlapping execution. When the executor is unreachable, mark
reconciliation blocked rather than invent a success or take over disk operations.

## Logs and interaction

```sh
nixmox tasks list --host cedar
nixmox tasks show task_01 --json
nixmox tasks watch task_01
nixmox tasks progress task_01 --json
nixmox tasks logs task_01 --follow
nixmox tasks cancel task_01 --wait
nixmox tasks retry task_01 --wait
nixmox hosts refresh cedar --wait
nixmox hosts diagnostics cedar --wait
nixmox hosts shutdown cedar --confirm-host cedar --wait
```

Types: `task.cancel`, `task.retry`, `host.refresh`, `host.diagnostics`, `host.shutdown`.
Retry may return `CONFIRMATION_REQUIRED`; destructive operations must reopen their
review flow. Shutdown success reports scheduled/observed state separately.

Browser: Tasks → select task → Overview / Logs / Result → Cancel or Retry.
TUI: Tasks → Enter → same tabs; actions in the task menu. Host pages link to the
same task record, including remote work. Filters are host, kind, state, and time.

`GET /tasks/{id}/logs?offset=0&limit=65536` returns offset, bytes as base64, and next
offset. Persist log bytes before broadcasting chunks. Gaps are fetched from the
owner; unreachability is shown as unavailable, not EOF. Downloads may stream the
full retained log. Retention status and truncation are explicit.

Read `/tasks/{id}/progress` and paginated `/tasks/{id}/activities`; activity detail
is `/tasks/{id}/activities/{activity_id}`. Watch is read-only and follows the common
event stream. Include counters and a progress link in task details, not an unbounded
activity list in every host snapshot.

Acceptance: interrupt each admission boundary; redeliver identical commands;
restart while queued/running/cancelling; cancel remote work; lose a log chunk;
lose the executor after an effect. None may duplicate a destructive operation or
turn an unknown outcome into success.
