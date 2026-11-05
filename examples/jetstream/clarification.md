# JetStream example: state, controllers, interruption, and membership

This is the agreed behavior and data layout for the example, not a claim that the
current Go implementation already satisfies every case. Roles are `unjoined`,
`member`, and `member-stable`.

## 1. Core model

- **Task events** are delivered requests that create or update tasks.
- **Tasks** are durable state for processes tracked over time, not notifications.
- **Member desired records** contain intent; **member state records** contain observations.
- **Topology** is derived from member records. There is no stored topology plan,
  global executor owner, or document containing every task and member.
- A **controller** is an update/reconciliation function. Watch events, startup,
  retries, or a background reconciliation pass can invoke the same function.
- Controllers read current state rather than treating each event as a command
  that must execute exactly once.

## 2. JetStream storage layout

| Resource | Subject or key | Contents / purpose |
| --- | --- | --- |
| `TASK_EVENTS` stream | `tasks.<targetID>.<action>` | Durable requests addressed to a daemon |
| `TASKS` KV bucket | `<targetID>.<taskID>` | One task's immutable request and mutable progress |
| `MEMBERS` KV bucket | `<memberID>.desired` | One member's role and service intent |
| `MEMBERS` KV bucket | `<memberID>.state` | One member's observed services and effective role |
| Local files | Per-daemon data directory | Identity, endpoints, server storage, and cold-start information |

Use KV history for inspection and CAS for concurrent updates. KV history does not
replace the task-event stream or guarantee that a controller receives every
intermediate update.

### Task event

A requests that C demote:

```yaml
subject: tasks.C.demote
body:
  id: demote-C-1
  requestedBy: A
  target: C
  role: member
```

C has a durable consumer filtered to its task-event subjects. It creates the task
using KV `Create`, then acknowledges the event. Redelivery must find the same task
ID and verify the request matches, rather than create or restart a second task.
Task startup reconciliation also discovers existing unfinished tasks.

### Task record: `TASKS/C.demote-C-1`

```yaml
spec:
  requestedBy: A
  target: C
  action: demote
  role: member
state:
  roleUpdated: false
  topologyReconciled: false
  dataMoved: false
  primaryUpdated: false
  completed: false
  error: null
```

`primaryUpdated` records completion; it is not an instruction to update the
primary. These fields are checkpoints. They do not replace checking whether
prerequisites are still satisfied before a destructive action.

### Desired record: `MEMBERS/C.desired`

```yaml
role: member-stable
plannedTerminate: false
primary: true
bootstrap: false
transition: null
```

Role and primary intent are separate. Setting `role: member` requests demotion;
it does not immediately authorize stopping the primary.

### Observed record: `MEMBERS/C.state`

```yaml
role: member-stable
online: true
primary: true
primaryReady: true
bootstrap: false
bootstrapReady: false
observedDesiredRevision: 17
```

The observed revision identifies which desired record was reconciled. Readiness
must correspond to the relevant intent, not an old `true` value from another
server incarnation. Server identities and endpoints also belong in the member's
observed/persisted information; they are omitted above for readability.

## 3. Write ownership and atomicity

| Writer | Writes | Does not write |
| --- | --- | --- |
| Requesting daemon/API | Task-event messages | Another daemon's observed state |
| Target task-event consumer | Initial task record | Native service state |
| Target task controller | Its task progress; its member's desired role/primary intent | Other members' records |
| Local stop/start lifecycle | Its own `desired.plannedTerminate` | Membership role as a consequence of interruption |
| Local bootstrap-election function | Its own `desired.bootstrap` when election assigns/releases local responsibility | Another member's desired record |
| Local service controller | Native local services and its own observed state | Desired role or service intent |

The election function and service controller are distinct responsibilities even
if the same watcher invokes both. All daemons may calculate election results;
only the selected daemon requests its own bootstrap. An old owner requests its
own bootstrap retirement only after the handoff prerequisites are satisfied.

Atomicity is **per KV key**:

- Read the current record, merge the owned fields, and CAS its revision.
- On conflict, reread and recompute; never replace a newer record with a cached copy.
- Task role updates and bootstrap-election updates may share a desired key, so
  both must preserve fields owned by the other local function.
- A write to member desired state and a task-progress write are not one transaction.
  Reconciliation must detect that the first write already succeeded after a crash.
- Separate member keys do not provide an atomic snapshot of the entire topology.
  Revalidate relevant desired revisions and observed health before retirement.

No cross-key atomicity or exactly-once external NATS operation is assumed.

## 4. Watchers and reconciliation

| Trigger | Reconciliation it wakes |
| --- | --- |
| Task event | Target consumer creates/updates the identified task |
| Task record created or changed | Target's task controller |
| Any member desired role or `plannedTerminate` changes | Bootstrap eligibility/election on all daemons; affected task controllers |
| Local desired service intent changes | Local service controller |
| Member observed readiness changes | Tasks waiting on that member or topology prerequisite |
| Native NATS placement/readiness observation | Waiting local service/data-movement reconciliation |
| Startup or optional background retry | Reconcile current records and unfinished work again |

A watcher enqueues reconciliation; it should not contain the full workflow.
Repeated notifications are harmless. Local lifecycle operations are serialized
and idempotent. Waiting for another daemon means returning until an observation
or retry wakes the controller, rather than blocking the watcher indefinitely.

## 5. Membership, availability, and planned termination

| Value | Meaning | Effect on bootstrap calculation |
| --- | --- | --- |
| `desired.role` | Requested membership role | Only `member-stable` contributes |
| `state.role` | Effective role reached so far | Describes progress; does not hide a requested demotion |
| `state.online` | Last-observed/reported availability | Used to assess reachable candidates, not to infer a role change |
| `desired.plannedTerminate` | Explicit intent to stop this daemon | `true` excludes it from the future bootstrap calculation |
| `desired.primary` | Whether the local primary should run/be retained as a service | Staged separately from the role during demotion |
| `desired.bootstrap` | Whether this daemon should run a bootstrap | Only the responsible daemon changes its own intent |

A stop leaves membership role unchanged. An unexpected disconnect does not set
`plannedTerminate`, change membership, or create a topology transition by itself.

### Planned termination flag lifecycle

| Situation | Flag handling |
| --- | --- |
| Preparing a deliberate stop | Set own `desired.plannedTerminate: true` before optional preparation, when KV is writable |
| Stop without eviction/waiting | Publish the flag best effort; do not wait for handoff or relocation |
| KV unavailable during stop | Record local intent/warnings and stop anyway; no successful shared announcement is assumed |
| Unexpected process/network failure | No flag update can be assumed |
| New daemon instance starts | Clear local planned-termination intent; clear its shared flag once KV becomes writable |
| Shared KV lacks quorum at startup | Restore retained peers first; do not require clearing the shared flag before they start |

The flag is cleared on startup, not retained as a reason to remain permanently
ineligible. Clearing it does not declare server readiness: observed readiness is
reported separately after restoration.

**Policy change from the earlier draft:** the bootstrap equation now explicitly
excludes planned departures. Therefore a planned departure from three stable
members can trigger bootstrap preparation. This supersedes the earlier blanket
rule that stopping one of three must never start a bootstrap. An unannounced
outage still does not change that equation.

## 6. Deterministic bootstrap calculation

Derive the future set from all member desired records:

```text
futureStable = members where
    desired.role == member-stable
    AND desired.plannedTerminate == false
```

| Size of `futureStable` | Bootstrap intent |
| ---: | --- |
| 0 | No new eligible owner; do not invent a new cluster or treat this as cluster destruction |
| 1 | Bootstrap needed on the eligible surviving member |
| 2 | One bootstrap needed on an eligible owner |
| 3 or more | Bootstrap not needed after the future primaries/placement are healthy |

For a new election, candidates must also be reachable, capable of serving, and
not being removed/demoted. Preserve a still-eligible existing owner; otherwise
choose the **highest ID** among eligible candidates. An offline flag alone does
not remove a stable member from the equation, but an unreachable member cannot
be selected to perform a new startup.

Every daemon can compute this result. Only the elected daemon sets its own
`desired.bootstrap: true`. When that desired-key update is observed, its service
controller starts bootstrap and reports `state.bootstrap` and readiness.

New eligibility does not authorize immediate retirement of the old bootstrap.
First verify replacement readiness, actual placement, and the resulting metadata
configuration. Temporary dual bootstraps are allowed during transfer.

## 7. Replication and quorum

Replication policy and bootstrap eligibility are separate decisions. The new
`plannedTerminate` flag changes bootstrap eligibility; it does not by itself
change configured stream replication or permanently remove primary peers.

| Desired member-stable role count, including planned/offline members | Critical data replication policy |
| ---: | --- |
| 1 | R2 |
| 2–4 | R3 |
| 5 or more | R5; noncritical data can remain R3 |

Permanent role changes drive replication-policy updates. Temporary interruption
may leave configured copies unavailable; it does not silently lower the factor.

| Replicas | Majority required | Unavailable copies normally tolerated |
| ---: | ---: | ---: |
| R1 | 1 | 0 |
| R2 | 2 | 0 |
| R3 | 2 | 1 |
| R5 | 3 | 2 |

These assume communicating, caught-up survivors and successful leader election.
Metadata, streams, and replicated consumers have separate consensus groups. Do
not infer all their health from one `ready` boolean or a daemon count.

A bootstrap is an extra server, not an extra member. A primary and bootstrap on
one daemon are separate consensus peers but share one failure domain.

### Evacuation completion is an observation, not an API acknowledgement

Native NATS does not interpret our member KV records. A successful evacuation
request means it accepted the request, not that data is safe to stop serving.

After requesting movement:

1. Read the relevant current member intent, including every planned termination
   and requested demotion/removal.
2. Inspect native stream/consumer placement and health.
3. Verify healthy copies/quorum on the intended surviving servers, not merely
   that replicas exist somewhere in the current cluster.
4. If placement still relies on another departing server, reconcile again;
   do not mark `dataMoved` or authorize permanent retirement yet.
5. Revalidate when relevant desired state changes.

Native placement may temporarily choose a server another task intends to retire.
That is acceptable as an intermediate state, not as proof that evacuation has
completed safely. Placement restrictions should be supplied to NATS where needed;
owned application records alone do not constrain its destination choices.

## 8. Demotion walkthrough: C leaves three stable members

Assume A wins the highest-eligible-ID election. Initially A/B/C run primaries,
there is no bootstrap, and critical data is R3.

| Step | Actor | Write or effect | Next trigger |
| ---: | --- | --- | --- |
| 1 | A | Publish `tasks.C.demote`, ID `demote-C-1` | C's event consumer |
| 2 | C event consumer | Create `TASKS/C.demote-C-1`, progress false; acknowledge event | Task watch/startup reconciliation |
| 3 | C task controller | CAS `C.desired.role = member`, retain `primary = true`; then record `roleUpdated = true` | Desired-member watchers |
| 4 | All election functions | Derive two future stable members and elect A | Only A writes its own bootstrap intent |
| 5 | A | CAS `A.desired.bootstrap = true` | A's desired-service watcher |
| 6 | A service controller | Start bootstrap, verify readiness, update only `A.state` | Member-state watchers |
| 7 | C task controller | Verify required topology/readiness; record `topologyReconciled = true` | Task reconciliation |
| 8 | C task controller | Request data relocation; verify A-primary/B-primary/A-bootstrap placement is healthy; record `dataMoved = true` | Native observations/task reconciliation |
| 9 | C task controller | Recheck prerequisites; CAS `C.desired.primary = false` | C's desired-service watcher |
| 10 | C service controller | Keep client connectivity; retire primary metadata peer and stop primary; update `C.state` | Member-state watchers |
| 11 | C task controller | Observe matching member/service completion; set `primaryUpdated` and `completed` true | Task-status watchers |

R3 remains R3 because permanent membership changes from three stable members to
two. Until prerequisites succeed, C keeps its primary even though its desired
role is already `member`. Errors report blocked progress; they do not rewrite
C's desired role back to `member-stable`.

## 9. Concurrent membership changes

Concurrent demotions are not forbidden merely because there is more than one
controller. Each target owns its records and local side effects. There is no
required global executor owner or single topology-plan key.

For concurrent B/C demotion from A/B/C:

| Observation | Required response |
| --- | --- |
| B and C both desire `member` | Derive A as the future stable member; do not consider B/C permanent destinations |
| A's bootstrap becomes ready | Both tasks may observe the prerequisite; neither owns A's state |
| Future membership is one stable member | Reconcile critical-data replication to R2 |
| B's evacuation initially places data on C | B's task must not treat that as final safe placement |
| Healthy final copies are on A-primary and A-bootstrap | Tasks may advance their local retirement steps after revalidating metadata safety |
| Intent changes while movement is underway | Recompute and recheck; prior progress flags are not lasting authorization |

Per-record CAS prevents lost writes, **not** arbitrary cluster-wide safety races.
Shared NATS configuration changes must converge on the same desired factor and
placement, and controllers must handle native reconfiguration conflicts/retries.
If all members request demotion, there is no viable future topology: do not
complete destructive retirement merely because each individual key update was
valid. Explicit cluster destruction is separate.

## 10. Interruption scenarios under the updated flag rule

`A/B/C` denote primaries; `Ab/Bb` denote bootstraps. Successful preparation assumes
quorum remains available long enough to perform it.

| Situation | Announced intent | Bootstrap behavior | Stop / quorum result |
| --- | --- | --- | --- |
| Three stable members; C crashes | No new flag | No bootstrap based solely on disappearance | A/B normally retain R3 and metadata quorum |
| Three stable members; C plans to stop | C sets `plannedTerminate = true` | Future set is A/B; elect/prepare bootstrap on an eligible survivor | Preparation is best effort; R3 stays configured |
| Same planned stop without eviction waiting | Flag published if possible, then stop | Other controllers may notice intent and prepare; caller does not await it | No promise preparation finishes before shutdown |
| Two of three stop without successful preparation | Flags may be unavailable or preparation incomplete | Do not claim successful replacement | Remaining peer may lose quorum; stop is still allowed |
| Two stable members, bootstrap on A; B plans to stop | B sets flag | A remains eligible owner | A/Ab can retain quorum after B stops |
| Two stable members, bootstrap on A; A plans to stop | A sets flag | Elect B and attempt Ab → Bb transfer | Successful transfer lets B/Bb continue; failure does not veto stop |
| Bootstrap owner A disappears unexpectedly | No new flag | No automatic handoff inferred from disconnection alone | B alone may lack quorum |
| Only stable member plans to stop | Its flag leaves no future candidate | No replacement election possible | Preserve assigned primary/bootstrap for restart; cluster goes offline |
| Planned member returns | Clear its flag when shared writes are possible | Recompute future set; preserve eligible ownership or retire surplus bootstrap only after readiness | Same retained primary catches up; no automatic role change |
| Client-only member stops | Its own flag can change | Stable-member equation unaffected | No server quorum responsibility removed |

Unlike the earlier draft, two announced stops do not guarantee quorum loss:
completed bootstrap/data preparation may preserve availability. Equally, optional
preparation never guarantees availability. The actual peer/replica configuration
and completion of handoff determine the result.

## 11. Best-effort stop versus permanent retirement

| Operation | Preparation | On failure | Primary peer/storage |
| --- | --- | --- | --- |
| Stop without eviction | Announce planned termination if possible; no wait for handoff/movement | Stop anyway | Preserve metadata membership, identity, and directory |
| Stop with eviction | Announce flag; bounded bootstrap/data preparation; observe health | Report incomplete work and stop anyway | Preserve metadata membership, identity, and directory |
| Unplanned interruption | None possible | Native recovery where quorum allows | Retained persisted state is used on return |
| Demotion/removal | Required topology/data preparation before permanent retirement | Do not report completed membership transition | Permanently retire primary peer; removal prevents automatic re-enrollment |
| Obsolete bootstrap retirement | Verify surviving topology/placement first | Do not claim completed handoff | Remove obsolete bootstrap peer and its retired store |

Stopping must not use `DisableJetStream()` or metadata-peer removal on a primary
that is intended to return. Those are permanent retirement operations.

A failed transfer may leave two bootstraps temporarily present. Persist actual
progress, not just the intended owner. For example, A/B/Ab/Bb has four metadata
peers: stopping A and Ab before retiring Ab leaves only two of four, not a
majority. A completed transfer retires Ab, leaving A/B/Bb, then permits A's
primary to stop with two of three remaining.

## 12. Cold start and clearing planned termination

A returning daemon is a **new instance** loaded from its data directory. Persist:

- Daemon and cluster identity, role, and known endpoints/routes.
- Primary identity, ports, storage path, and retained metadata state.
- Assigned bootstrap identity/ownership/storage and completed retirement state.
- Last observed topology and incomplete transitions/handoffs.
- Permanent-removal state so removal does not become automatic enrollment.

| Startup condition | Behavior |
| --- | --- |
| Client-only member | Reconnect and clear its own shared planned-termination flag when writable |
| Retained stable primary | Start a new server object using the same identity/directory; clear flag and report current observations |
| Owner after completed bootstrap transfer | Restore primary only; do not resurrect the retired bootstrap |
| Only stable member owning primary and bootstrap | Restore both before waiting for JetStream readiness |
| Multiple retained peers must return to restore quorum | Start peers from local state first, then attach to shared stores and clear flags |
| Permanently removed identity | Stay unjoined until explicit enrollment |
| Incomplete handoff / contradictory local and shared state | Reconcile the recorded progress; do not invent a new cluster or silently assume completion |

Clearing `plannedTerminate` is startup intent, not readiness. Do not make its KV
write a prerequisite for starting the servers needed to restore KV quorum.

## 13. Implementation scope and alignment

The Go example is work in progress. Align it with this document rather than
preserving earlier implementation shortcuts:

- Split members and tasks into the keys above, with separate desired/observed keys.
- Replace the single cluster document/topology-plan/executor concepts with owned
  records, derived topology, and idempotent reconciliation.
- Keep task events distinct from task process state.
- Separate election/desired-intent writers from observed-state service controllers.
- Use `plannedTerminate` explicitly; do not infer it from connectivity.
- Permit concurrent tasks only with aggregate survivor/placement checks; CAS alone
  is not the safety mechanism.
- Check data and metadata health after native requests; do not equate API acceptance
  with completed evacuation.
- Preserve best-effort stop and true disk-backed cold construction.
