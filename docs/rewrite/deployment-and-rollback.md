# Deployment and rollback

Deploy a configuration to an installed host from any console. Fresh installation
can start here and reuses [onboarding](host-onboarding.md) for preparation, disk
confirmation, and installation. Ordinary Apply never formats disks.

## Operation boundaries

| Action | CLI / command type | Behavior / backend |
| --- | --- | --- |
| Apply latest | `nixmox apply [host]` or `deploy apply`; `deployment.apply` | Update an existing NixOS system through Nix build/rebuild and activation tools; never partition/format |
| Install | `nixmox deploy install <host>`; `deployment.install` | First installation or fresh replacement of an existing system; enter installer if needed, then use nixos-anywhere/Disko after disk review |
| Reboot into installer | `nixmox hosts reboot <host> --into installer`; `host.enter_installer` | Enter the installer only; no erasure or installation |
| Prepare configuration | `nixmox onboarding inspect/prepare/commit` | Collect facts, render/review configuration, and commit; never install |

Installation has one public workflow and command type, regardless of whether the
host has been installed before. Onboarding supplies preparation and confirmation
services to that workflow; its execution phases do not become separate install APIs.
Apply requires a running NixOS system with verified rebuild/activation capability;
it is unavailable in the installer or on a non-NixOS bootstrap target and never
falls back to installation. A bootstrap agent on NixOS may Apply a configuration
that enables the permanent agent. Installer-entry incompatibility does not block Apply.

## Candidate and deployment objects

```json
{
  "schema_version": 1,
  "id": "cand_01",
  "source": {
    "repository_id": "r_lab", "configuration": "cedar", "ref_id": "ref_cedar",
    "commit": "1111111111111111111111111111111111111111",
    "flake_directory": ".", "flake_attribute": "nixosConfigurations.cedar"
  },
  "builder_id": "n_birch",
  "system": "x86_64-linux",
  "state": "built",
  "output_path": "/nix/store/11111111111111111111111111111111-nixos-system-cedar-new",
  "nar_hash": "sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
  "input_lock_digest": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  "comparison": {"diff_id": "diff_01", "host_id": "n_cedar", "generation": 12, "stale": false}
}
```

Store paths/hashes are fixture values. An actual candidate records verified output
metadata and all source dependencies; branch names alone are insufficient.

```json
{
  "id": "dep_01",
  "task_id": "task_01",
  "target_id": "n_cedar",
  "candidate_id": "cand_01",
  "mode": "switch",
  "previous_generation": 12,
  "new_generation": 13,
  "activation": "succeeded",
  "observation": "pending",
  "running_generation": null,
  "boot_generation": 13
}
```

Do not equate activation exit status with observed deployment success. Track desired
binding, built candidate, active generation, and boot generation separately.

## Apply latest

**Apply latest** is the primary action on each host-list row and host detail page.
It runs one task: resolve and pin source → validate → build → diff → approve →
transfer → activate/stage → observe. Users do not need to manage candidates or
navigate between screens. Builder selection and Now/Next boot mode are options.

```sh
# On cedar: latest configuration → build → preview/diff → confirm → switch.
nixmox apply
# Same workflow without the confirmation pause.
nixmox apply --yes
# Explicit target and remote builder, when needed.
nixmox apply cedar --builder birch
```

`nixmox apply [host]` and `nixmox deploy apply [host]` are aliases with identical
defaults. Examples with additional arguments demonstrate overrides, not required
boilerplate.

| Omitted argument | Default |
| --- | --- |
| Host | Current node, identified through the local daemon's stable node ID |
| Repository/configuration/ref | Target's assigned configuration and tracked GitOps ref |
| Candidate | Build the pinned latest revision; reuse valid available build outputs |
| Builder | Target if build-capable and available; otherwise an eligible available cluster builder |
| Mode | `switch`: activate now and select for subsequent boot |
| Approval | Show preview/diff and ask for confirmation; `--yes` auto-approves |
| Wait | Apply follows progress through review and completion; `--detach` returns task IDs immediately |

Resolve an omitted host only through the local daemon, never the shell hostname,
working directory, or a remote gateway's identity. When using a remote endpoint
or without a local daemon, require an explicit host. Print resolved target,
configuration, pinned revision, builder, and mode before build progress. An explicit
builder is honored or fails clearly; if automatic selection finds none, report
`BUILDER_UNAVAILABLE`. The preview concerns the exact built candidate that will
be switched. Declining confirmation cancels before activation.

Browser/TUI: Hosts → cedar row or detail → Apply latest → options with
**Auto-approve** toggle (off by default) → Start. The same panel shows build
progress, package/configuration diff, and deployment outcome. With the toggle off,
pause at the diff with Approve/Cancel; with it on, continue automatically while
keeping the diff available. No second confirmation follows auto-approval.

“Latest” means the latest upstream commit after fetch for an upstream-tracking
binding, or the latest accepted local-change head for an unpublished binding.
It does not rebase local changes. Resolve and pin the commit before task admission;
show that revision throughout execution. A failed fetch is an error, not permission
to silently deploy a cached upstream revision. Later Git changes do not retarget
the task. Missing configuration bindings direct the user to configuration selection.

Omitting `candidate_id` selects the host's latest binding. Supplying it uses that
exact built candidate and skips building; combining it with `builder_id` is invalid.
`auto_approve` defaults to false; `--yes`
sets it to true for this invocation only. It approves deployment of the pinned
candidate to this target in the selected mode, not future Git changes, conflict
overrides, installation disk erasure, or USB writes. Validation, output trust,
compatibility, target exclusion, and observation gates still apply.

```json
{
  "id": "cmd_apply_latest_01",
  "type": "deployment.apply",
  "target_id": "n_cedar",
  "expected_versions": {"host:n_cedar": 7},
  "payload": {"builder_id": "n_birch", "mode": "switch", "auto_approve": true}
}
```

The approval choice is persisted with the task. Manual review uses `blocked` with
reason `APPROVAL_REQUIRED`; reconnecting through any console resumes that review.
Apply waits by default (`--wait` is accepted but redundant). Interactive execution
shows the diff and prompts once. Noninteractive waiting
without `--yes` returns exit 4 when approval is required, printing task/diff IDs;
it never approves implicitly. To approve later:

```sh
nixmox deploy approve task_01 --diff diff_01 --diff-version 1 --wait
```

Approval references the exact diff version, candidate, baseline, target, and mode.
The server resumes the existing task, not a second deployment. Keep its source,
candidate, and diff pinned while awaiting approval; do not hold a target execution
lock during user review. Cancel releases task retention pins when safe.

## Preview and explicit candidates

```sh
nixmox deploy preview cedar --repository lab --configuration cedar --ref ref_cedar --wait
nixmox deploy build cedar --repository lab --configuration cedar --ref ref_cedar --builder birch --wait
nixmox deploy candidate show cand_01
nixmox deploy diff cedar --candidate cand_01 --wait
nixmox deploy apply cedar --candidate cand_01 --mode switch --wait
# Alternative: change next boot only, without activating now.
nixmox deploy apply cedar --candidate cand_01 --mode boot --wait
nixmox hosts reboot cedar --confirm-host cedar --wait
```

Preview performs source diff and evaluation without a full system build. Build
produces an output without activation and enables package/generated-configuration
comparison. It cannot predict all activation effects. Invalidate comparisons when
the candidate or observed target generation changes.

| Command type | Payload |
| --- | --- |
| deployment.preview | host_id, source selector |
| deployment.build | host_id, source selector, builder_id |
| deployment.diff | host_id, candidate_id, optional baseline_generation_id |
| deployment.apply | optional candidate_id (otherwise latest binding), optional builder_id for builds, mode (default switch), auto_approve (default false); target_id on envelope, expected host version |
| deployment.approve | task_id, diff_id, diff_version; resumes the original task after checking its approval evidence |
| deployment.rollback | generation_id, mode; target_id, expected host version |
| host.reboot | host_id, confirm_host |

Read `/candidates/{id}`, `/deployments/{id}`, `/hosts/{id}/generations`.
The service resolves a selector to a commit before task admission. Unknown source,
platform mismatch, missing builder capability, or conflicting target work returns
a specific error before activation.

| Step | Browser | TUI |
| --- | --- | --- |
| Preview | Hosts → cedar → Deployment → Preview | Hosts → cedar → Deployment → Preview |
| Select builder | Build → Builder: birch | Build → builder selector → birch |
| Inspect candidate | Candidate → packages/configuration diff | Candidate → Diff tabs |
| Apply | Hosts row/detail → Apply latest → options / Auto-approve → Start → inline diff approval if needed | Same action, toggle, progress, and inline approval |
| Watch | Task progress and deployment state | Task detail with Logs/Result |

## Required package diff

Before applying a built candidate, show an NH-style package summary: additions,
removals, and old → new versions. This is structured API data shared by browser,
TUI, and CLI, not terminal output embedded in the browser.
[NH package diff UX](https://github.com/nix-community/nh)

Compare the target's observed running system closure with the exact candidate
closure, even when another node built it. Do not compare with the builder's own
system or assume the previous Git commit describes what is actually running.
Offer an explicit recorded-generation baseline for historical comparisons and
use the same diff for rollback review.

| Category | Display |
| --- | --- |
| Added | Package name and new version |
| Removed | Package name and old version |
| Version changed | Package name, old version → new version; upgrade/downgrade only when reliably determined |
| Output changed, same version | Same name/version, changed store output; do not imply it was compiled locally |
| Ambiguous/unversioned | Available names, versions, and store paths without inventing a pairing |

Default scope is the system closure, including dependencies, labeled accordingly.
Where available, distinguish user-facing system packages from dependencies.
Multiple versions/outputs must remain distinguishable; do not key records solely
by package name. Stable ordering, search, and category filters are required.
Show unchanged counts without flooding the main view with unchanged rows.

### Diff object example

```json
{
  "schema_version": 1,
  "id": "diff_01",
  "version": 1,
  "host_id": "n_cedar",
  "candidate_id": "cand_01",
  "baseline": {
    "kind": "running",
    "generation_id": "gen_12",
    "output_path": "/nix/store/00000000000000000000000000000000-nixos-system-cedar-old",
    "observed_at": "2026-10-20T12:00:00Z"
  },
  "candidate_output": "/nix/store/11111111111111111111111111111111-nixos-system-cedar-new",
  "scope": "system_closure",
  "state": "ready",
  "stale": false,
  "summary": {"added": 1, "removed": 1, "version_changed": 1, "output_changed": 0, "ambiguous": 0, "unchanged": 250},
  "closure_size": {"before_bytes": 2147483648, "after_bytes": 2202009600, "delta_bytes": 54525952},
  "changes": [
    {"id": "change_01", "kind": "added", "name": "ripgrep", "old": [],
     "new": [{"version": "14.1.0", "store_path": "/nix/store/22222222222222222222222222222222-ripgrep-14.1.0"}]},
    {"id": "change_02", "kind": "removed", "name": "wget",
     "old": [{"version": "1.24.5", "store_path": "/nix/store/33333333333333333333333333333333-wget-1.24.5"}], "new": []},
    {"id": "change_03", "kind": "version_changed", "name": "openssl",
     "old": [{"version": "3.3.1", "store_path": "/nix/store/44444444444444444444444444444444-openssl-3.3.1"}],
     "new": [{"version": "3.3.2", "store_path": "/nix/store/55555555555555555555555555555555-openssl-3.3.2"}]}
  ],
  "next_cursor": null
}
```

Paths and versions above are fixtures. Summary counts cover the complete comparison;
`changes` is paginated. Closure size counts unique paths per closure, using comparable
store-size metadata; it is not download size or guaranteed disk space reclaimed.
Unavailable sizes or versions are null. Cache the result by both output identities
and comparison schema, preserving it with deployment history.

### Interface flow

```text
cedar: running generation 12 → candidate cand_01
System closure: 1 added • 1 removed • 1 version changed • 250 unchanged

+ ripgrep   added            14.1.0
- wget      removed          1.24.5
~ openssl   version changed  3.3.1 → 3.3.2

Closure: 2 GiB → 2 GiB + 52 MiB  (not download size)
[Packages] [Configuration] [Source] [Apply now] [Stage for boot]
```

Browser/TUI: Candidate → Diff → Packages; display summary and searchable rows,
expand a row for store paths, and retain separate Source and Configuration tabs.
Use text labels as well as symbols/color. Review includes this summary for apply,
boot staging, and rollback. Auto-approved apply still records and displays the
summary but does not pause for review. CLI commands:

```sh
nixmox deploy diff cedar --candidate cand_01 --wait
nixmox deploy diff show diff_01 --json
```

`GET /api/v1/diffs/{id}` returns the summary and first page;
`GET /api/v1/diffs/{id}/changes?kind=version_changed&cursor=...` reads further rows.
Candidate builds compute the default comparison once target/candidate metadata is
available; `deployment.diff` refreshes or selects a different baseline without
rebuilding an already available candidate.

### Availability and correctness

Lightweight pre-build validation still performs no full build. Until a candidate
closure is available, show “Package diff requires a built candidate” and offer Build;
do not substitute a Git text diff and label it a package diff. Missing baseline
metadata is an unavailable comparison, not zero changes. If a cached baseline is
used while the target is unreachable, show its timestamp and freshness explicitly.

Recheck the target output before activation. If it differs from the reviewed
baseline, mark the diff stale and recompute under target exclusion. Manual approval
must release target exclusion and return to review; auto-approval may continue
with the new recorded diff only
for the same pinned candidate, target, and mode. Explicit version preconditions
still fail on conflict. An unavailable comparison blocks activation in either mode.
Changes in outputs/versions invalidate
evidence even when a source branch has the same name. Package equality does not
prove identical generated configuration, service behavior, or activation effects.

Acceptance: added/removed/version-changed packages; same-version output changes;
multiple versions and unknown versions; remote builder versus target baseline;
unchanged package set with changed configuration; rollback direction; stale baseline;
pagination and identical summaries across all interfaces.

## Required build and deployment progress

Provide the depth of feedback associated with `nh os switch`: structured download
and build activity, not just a spinner and a raw log tail. NH uses nix-output-monitor
for build-tree displays; this is a UX reference, not a requirement to run NH or
scrape its terminal rendering.
[NH build display](https://github.com/nix-community/nh)

Browser, TUI, and interactive CLI must show:

| Activity | Required information |
| --- | --- |
| Plan | Known downloads, derivations to build, already available paths, and whether totals are still being discovered |
| Downloads | Package/path name, queued/active/completed/failed, actual cache/source, transferred bytes, known total, measured speed, retry/fallback |
| Builds | Derivation/package name, queued/running/completed/failed, executing node, elapsed time, reported build phase, per-build log link |
| Target transfer | Builder/source and target, paths and bytes copied, known total, measured speed, verification status |
| Activation | Named activation/service steps when reported, elapsed time, errors, and running versus boot-staged outcome |
| Observation | Waiting for agent/reboot, elapsed time, last report, and verified expected system |

Use actual transfer events for cache attribution; listing configured substituters
is not evidence that they served a download. Strip credentials and sensitive query
parameters from displayed source URLs. Show cache misses and fallback to local
builds explicitly. Nix store paths and derivations are canonical identifiers;
friendly package names are labels and are not guaranteed to map one-to-one.

### Progress snapshot example

`GET /api/v1/tasks/task_build_01/progress` returns a versioned snapshot:

```json
{
  "schema_version": 1,
  "task_id": "task_build_01",
  "version": 42,
  "observed_at": "2026-10-20T12:00:00Z",
  "phase": "building",
  "freshness": "live",
  "plan_complete": false,
  "downloads": {
    "completed_paths": 18,
    "total_paths": 24,
    "active_paths": 2,
    "bytes_received": 268435456,
    "bytes_total": 1073741824,
    "byte_basis": "download",
    "bytes_per_second": 8388608,
    "eta_seconds": null,
    "totals_kind": "known_so_far"
  },
  "builds": {
    "completed_derivations": 2,
    "total_derivations": 12,
    "active_derivations": 2,
    "failed_derivations": 0
  },
  "target_transfer": null,
  "active_activity_ids": ["activity_download_01", "activity_build_01"]
}
```

The activity IDs above are a bounded preview; obtain the full list through the
paginated activity endpoint. An activity detail example:

```json
{
  "id": "activity_download_01",
  "task_id": "task_build_01",
  "attempt": 1,
  "kind": "download",
  "state": "running",
  "node_id": "n_birch",
  "name": "example-package",
  "store_path": "/nix/store/00000000000000000000000000000000-example-package",
  "derivation_path": null,
  "source": {"kind": "cache", "url": "https://cache.nixos.org"},
  "progress": {"completed": 33554432, "total": 67108864, "unit": "bytes", "byte_basis": "download"},
  "bytes_per_second": 4194304,
  "elapsed_seconds": 8,
  "build_phase": null,
  "log_ref": null
}
```

Build activities use `kind: "build"`, their derivation path, executing node, reported
phase, and `log_ref`. Unknown totals/rates/phases are null, not invented zeroes.
Distinguish download bytes from unpacked NAR sizes and builder-to-target transfer;
do not combine unlike byte totals into one percentage.

### Bars and interface behavior

```text
Build cedar on birch                         00:42 elapsed
Downloads  [#####---------------] 25%        256 MiB / 1 GiB known • 8 MiB/s
  example-package   cache.nixos.org         32 / 64 MiB • 4 MiB/s
Builds     [###-----------------] 2 / 12     2 running • discovering dependencies
  linux             birch                   building • 00:31
Transfer → Activation → Observe              waiting
[Activity] [Logs] [Cancel]
```

Show download/transfer bars from comparable byte counters and build bars from
completed/known derivation counts. Label build counts as work items, not elapsed-time
estimates. Discovered work may increase totals; recompute and mark the revised plan.
Unknown totals use indeterminate bars with measured counts/speed. Do not assign
arbitrary fixed weights to phases or claim an accurate whole-operation percentage.
The phase tracker reaches success only after the required observation gate.

Calculate rates from monotonic byte deltas and elapsed time, smoothing over a short
window; reset the sample on counter resets/retries. Only show estimated time when
total and rate are usable. Pause live rates/ETA on stale data and label last-known
values. Download completion does not imply unpacking, verification, or build success.
Count completed work once per path/derivation; failed-attempt bytes must not inflate
completion percentages. Keep retry traffic separate from useful-work counters.

Browser/TUI: Task → Progress contains the bars, active activities, cache sources,
and expandable completed/failed lists; Logs remains separately available. Host
deployment summaries show the same aggregate. `deploy build/apply --wait` renders
this view interactively; `nixmox tasks watch task_build_01` attaches later.
`nixmox tasks progress task_build_01 --json` returns the snapshot for automation.

These requirements also apply to build/transfer stages in installation, media
creation, and rebuilds for rollback. Missing telemetry must be labeled explicitly;
raw logs alone do not satisfy progress acceptance on supported Nix versions.

## Execution contract

```text
pin source → validate → build → verify output → diff → approval
  → acquire target lock → recheck baseline/approval → transfer → activate/stage → observe
```

The builder materializes the pinned repository, evaluates the named configuration,
and produces the NixOS system closure. The target verifies transferred output
integrity/trust and records the intended generation before activation. Serialize
apply, rollback, and installation effects on a target.

Use standard Nix build/rebuild tooling for ordinary deployment: `nix build` for
the pinned system output and the NixOS activation/bootloader tools for that exact
output. A nixos-rebuild adapter must preserve this separation: activation cannot
resolve a newer ref or build a different candidate after diff approval. Do not run
nixos-anywhere or Disko formatting phases as part of Apply or rollback.

`switch` activates now and selects the system for subsequent boot. `boot` updates
the boot selection only and reports staged, not running. Reboot is explicit.
Default observation gate: the same authenticated node reports the expected output
as running and its management service is responsive. Application-specific checks
are optional future gates, not inferred from agent liveness.

On loss of connection, mark observation unknown/reconciling. Reconnect queries
running/profile/boot state and the execution receipt. Do not reactivate merely
because the response was lost. Cancellation during activation reports effects and
reconciles; it does not silently revert or claim nothing changed.

## Rollback

```sh
nixmox deploy generations cedar
nixmox deploy rollback cedar --generation gen_12 --mode switch --wait
```

Browser: cedar → Deployment → History → generation 12 → Roll back → review.
TUI: same History list/action. Show source commit, timestamp, output availability,
and running/boot consequences before submission.

Use a retained output; if absent, return `OUTPUT_UNAVAILABLE` and offer a separate
rebuild task. Pin five recent generations by default plus active, boot, requested
rollback, and task-referenced outputs. Do not garbage-collect referenced source.
Rollback changes system generation, not application data or Git history. Keep
source tracking unchanged and display desired/running drift. An older daemon must
not start against an incompatible migrated state format: compatibility is a gate.

## Reboot into installer and fresh install

Expose **Reboot into installer** and **Install** on host detail → Deployment
and in the host action menu in browser/TUI. Reboot into installer changes the boot
environment only; it never authorizes erasure. Install orchestrates entry,
configuration selection/preparation, destructive review, installation, and verified
return. Keep the same task panel and link its child tasks rather than sending the
user through separate tools. `apply --yes` cannot approve a fresh installation.

```sh
# Enter the retained/prepared installer through a normal reboot; stop there.
nixmox hosts reboot cedar --into installer --confirm-host cedar --wait
# Guided fresh installation; defaults to the assigned configuration's latest ref.
nixmox deploy install cedar --wait
# With an already reviewed/committed preparation, use its exact source revision.
nixmox deploy install cedar --preparation prep_01 --builder birch --confirm-device /dev/disk/by-id/nvme-EXAMPLE --wait
```

The first command submits `host.enter_installer` with method `reboot`. Install
submits `deployment.install` with `host_id`, `method` (default `auto`), and optional
`installer_id`, `configuration_id`, `preparation_id`, `builder_id`, and the bound
`confirmation`. A preparation must belong to this host, match any explicit
configuration, and reference a committed source; revalidate observations before
effects. Without a preparation, guide the user through the shared onboarding forms.
With no assigned configuration, require selection/creation before proceeding.
`--task task_01` supplies optional `task_id` to resume that same installation with
new review input rather than create another task. Validate task version and current
review binding; changing the target or pinned source requires a new reviewed plan.
Use `--confirm-host` for entry approval and the bound preparation/device confirmation
for erasure. Browser/TUI submit these same resumptions with unique command IDs.
`--method reboot|kexec|auto`
overrides selection; `--installer` selects a registered installer. The parent task
pauses for disruptive boot approval and, later, fresh disk confirmation using the
onboarding contract. Interactive `--wait` guides these reviews; noninteractive
waiting returns `APPROVAL_REQUIRED`/exit 4 with the pending review and task IDs.
Configuration defaults are suggestions; pin the chosen revision before building.
When already in a verified installer, skip entry and its boot approval. Otherwise
check entry compatibility before starting preparation/builds or disruptive actions.

| Entry method | Requirement |
| --- | --- |
| Already in installer | Verified compatible agent/environment, including USB/SD or kexec boot; no transition required |
| kexec | Verified capability, sufficient memory, and a compatible Nixmox installer kernel/initrd |
| reboot | Verified bootloader adapter and a retained or staged bootable installer independent of the erase targets |

`auto` prefers supported kexec; otherwise use a ready reboot route. Never assume
that an architecture guarantees kexec or a bootloader supports one-shot selection.
An explicit method fails clearly when unavailable. A failed/uncertain handoff is
reconciled, not retried by blindly switching methods. A running-OS target with
neither supported kexec nor a usable reboot route is **incompatible for installation**:
return `INSTALLATION_UNSUPPORTED` with the capability reasons before any effects.
Do not introduce another fallback or leave the task waiting for manual media boot.
This restriction does not prevent ordinary management or adoption. USB/SD remains
an independent entry path: a node booted into a compatible installer is evaluated
in that new environment. These rules apply to every machine profile.

Use the shared Nixmox installer definition for both media and kexec, packaged as
disk images or a kernel/initrd payload respectively. Reuse nixos-anywhere's kexec
phase with the pinned custom payload; do not implement another kexec loader or
use a stock environment missing the Nixmox agent. The SSH-bootstrap or running-OS
agent prepares the same protected handoff before invoking that phase. Agent
identity and task checkpoints persist across bootstrap → installer → installed OS.

Read `/hosts/{id}/installer` for entry capabilities and the selected recovery image:

```json
{
  "host_id": "n_cedar",
  "installer_id": "recovery_01",
  "image_id": "media_02",
  "methods": {"kexec": false, "reboot": true},
  "installation_supported": true,
  "unsupported_reasons": [],
  "boot_entry": "nixmox-installer",
  "state": "ready",
  "protected_device_ids": ["disk_sd"],
  "preserve_identity": true,
  "network_handoff": "ready"
}
```

Readiness requires verified image/boot files, boot selection support, and handoff
storage/network prerequisites; it is not proof that hardware has already booted it.
The bootloader adapter verifies/stages the entry, arms the next boot, records its
receipt, and restores normal selection after the installer acknowledges arrival.
Where one-shot selection is unavailable, use a tested persistent-selection/restore
adapter with explicit interrupted-boot recovery; otherwise that reboot method is
unsupported. If kexec is also unavailable, installation is incompatible for now.

```text
plan → stage installer + protected handoff → confirm entry → kexec/reboot
  → observe same node in installer → inspect/prepare → confirm erase targets
  → install → select installed system → reboot → observe expected output
```

Match identity, a one-use handoff ID, and an installer boot-session report, not IP
or ping alone. Persist checkpoints outside erase targets before reboot. Do not
restart a destructive child task after a lost response. A standalone reboot task
succeeds on verified installer arrival; the install task succeeds only
after the installed output is observed. Cancellation after entry leaves the node
in the installer and reports that fact. Installation resets system/data on the selected
targets while preserving identity by default; factory identity reset is separate.

Before entry, relocate/drain durable services under normal membership/quorum rules.
The installer has pending, non-voting participation, retaining host identity and
configuration intent. A single-node cluster needs a recoverable control-state/task
checkpoint on preserved storage and its standalone bootstrap path; if that cannot
be prepared, block before reboot/erasure rather than lose the only cluster state.
After successful installation/adoption, restore full membership through verification.

Acceptance: x86 kexec and normal reboot routes; Pi SD installer → NVMe install →
SD installer → NVMe reinstall; unsupported bootloader; missing media; stale handoff;
no wipe on entry; quorum preflight; single-node recovery; connection loss at every
handoff; no repeated partitioning; retained recovery survives apply/rollback/GC.
Verify the same install command from USB/SD, kexec, and an installed system; reject
unsupported entry without fallback/effects; Apply must never invoke formatting.

## Multiple targets and decisions

Proposed first slice: explicit single-target deployments. Proposed rollout command:
`nixmox deploy rollout --plan rollout.json --strategy serial --wait`, where the plan
maps target IDs to candidate IDs. Persist child tasks and stop advancing after
failed/unknown outcomes. Canary and bounded batches require defined health/pause
policies before implementation.

**Decision required:** cross-node closure transfer and signing trust; mixed-version
state rollback; rollout scope and automatic apply/rollback policy. These must not
be approximated by disabling output verification or replaying uncertain effects.

Acceptance: unpublished remote build; preview without activation; source pinned
during rebase; boot staging without changing running output; observed reboot;
rollback with/without retained output; executor interruption after activation;
concurrent apply rejection; removal of network path during deployment.
Verify Apply latest from host list/detail, default pause versus `--yes`, resumed
approval after console/worker restart, noninteractive exit 4, stale diff rejection,
auto-approved diff refresh, and source changes during a build without retargeting.
Verify bare `nixmox apply` on cedar targets cedar's assigned configuration, defaults
to switch and foreground review, matches its long-form alias, and requires an
explicit host when connected to a remote gateway.
Also verify cache source and speed, simultaneous builds/downloads, changing totals,
cache fallback/retries, target-transfer progress, stale/reconnected progress, and
matching browser/TUI/CLI displays. A finished byte bar must not mark a deployment
successful before activation/observation completes.
