# Deployment and rollback

Deploy a configuration to an installed host from any console. Fresh installation
can start here and reuses [onboarding](host-onboarding.md) for preparation, disk
confirmation, and installation. Ordinary Apply never formats disks.

## Operation boundaries

| Action | CLI / command type | Behavior / backend |
| --- | --- | --- |
| Apply latest | `nixmox apply [host]` or `deploy apply`; `deployment.apply` | Update an existing NixOS system through Nix build/rebuild and activation tools; never partition/format |
| Install | `nixmox deploy install <host>`; `deployment.install` | First installation or fresh replacement of an existing system; enter installer if needed, then partition/install after disk review |
| Reboot into installer | `nixmox hosts reboot <host> --into installer`; `host.enter_installer` | Enter the installer only; no erasure or installation |
| Prepare configuration | `nixmox onboarding inspect/prepare/commit` | Collect facts, render/review configuration, and commit; never install |

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

Omitting both `candidate_id` and `source` selects the host's latest binding. Supplying a candidate uses that
exact built candidate and skips building; combining it with `builder_id` or a
source override is invalid.
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

Persist approval policy with the task; manual review blocks with `APPROVAL_REQUIRED`.
Any console can resume it. Waiting/prompt/exit behavior follows the
[CLI contract](api-and-interfaces.md#cli-contract). Approve later with:

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
| deployment.preview | host_id, optional source selector; expected host version |
| deployment.build | host_id, optional source selector, optional builder_id; expected host version |
| deployment.diff | host_id, candidate_id, optional baseline_generation_id |
| deployment.apply | optional source selector or candidate_id, optional builder_id for builds, mode (default switch), auto_approve (default false); target_id on envelope, expected host version |
| deployment.approve | task_id, diff_id, diff_version; resumes the original task after checking its approval evidence |
| deployment.rollback | generation_id, mode (default switch); target_id, expected host version |
| host.reboot | host_id, confirm_host |

Preview/build/apply share `source: {repository_id?, configuration?, ref_id?, commit?}`.
CLI `--repository`, `--configuration`, `--ref`, and `--commit` populate those fields;
ref and commit are mutually exclusive. Omitted fields use the host binding only
within the same repository/configuration. Switching repository requires an explicit
configuration; switching configuration defaults to that configuration's tracked
ref, never the old host ref. No usable binding means required selection, not a
guess. Resolve the selector once using Apply's latest/fetch rules; explicit commits
remain pinned. Overrides do not reassign the host.
`deploy preview <host>` and `deploy build <host>` therefore need no source flags
when the host is bound. Build uses Apply's automatic builder rule unless overridden.
Preview performs no build and has no builder flag. Rollback defaults to `switch`;
`--mode boot` explicitly stages it for reboot.

Read `/candidates/{id}`, `/deployments/{id}`, `/hosts/{id}/generations`.
The service resolves a selector to a commit before task admission. Unknown source,
platform mismatch, missing builder capability, or conflicting target work returns
a specific error before activation.

Browser/TUI: Host → Deployment → Preview/Build → Candidate → Diff.
Tasks expose shared progress, logs, and results.

## Required package diff

Before Apply, show structured package additions, removals, and old → new versions
in all interfaces.

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
Use common `cursor`/`limit` pagination, optional `kind`, and `q` for case-insensitive
literal matching against package name, version, or store path. Filters affect rows,
not the complete summary. Order by stable change ID within the immutable comparison.
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
for the same pinned candidate, target, and mode. Admission checks the expected host
version; execution rechecks the reviewed binding and baseline, not unrelated host
health/inventory versions. An unavailable comparison blocks activation in either mode.
Changes in outputs/versions invalidate
evidence even when a source branch has the same name. Package equality does not
prove identical generated configuration, service behavior, or activation effects.

## Required build and deployment progress

Build and deployment feedback uses structured activities; raw log tails alone
are insufficient.

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
and consults the [cache gateway](cache-gateway.md) before building missing outputs.
The target pulls the exact candidate closure through its own local gateway from
available holders; Nix verifies integrity/trust. No SSH push is required. Record
the intended generation before activation. Serialize
apply, rollback, and installation effects on a target.

Use standard Nix build/rebuild tooling for ordinary deployment: `nix build` for
the pinned system output and the NixOS activation/bootloader tools for that exact
output. A nixos-rebuild adapter must preserve this separation: activation cannot
resolve a newer ref or build a different candidate after diff approval. Do not run
Disko formatting or fresh-install phases as part of Apply or rollback.

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
source tracking unchanged and display desired/running drift. Nixmox version/state
compatibility management is outside v1 scope; rollback does not promise application
data or daemon-state compatibility across versions.

## Reboot into installer and fresh install

Expose **Reboot into installer** and **Install** on host detail → Deployment
and in the host action menu in browser/TUI. Reboot into installer changes the boot
environment only; it never authorizes erasure. Install orchestrates entry,
configuration selection/preparation, destructive review, installation, and verified
return. Keep the same task panel and link its child tasks rather than sending the
user through separate tools. `apply --yes` cannot approve a fresh installation.

```sh
# Enter the retained/prepared installer through a normal reboot; stop there.
nixmox hosts reboot cedar --into installer --wait
# Guided fresh installation; defaults to the assigned configuration's latest ref.
nixmox deploy install cedar --wait
# With an already reviewed/committed preparation, use its exact source revision.
nixmox deploy install cedar --preparation prep_01 --builder birch --wait
```

Reboot into installer submits `host.enter_installer` with `method: reboot`.
Install accepts the following payload; CLI flags map to the corresponding fields:

| Field | Default / constraint |
| --- | --- |
| `host_id` | Required; matches envelope target |
| `method` | `auto`; CLI `--method auto\|reboot\|kexec` |
| `configuration_id` | `--configuration`; defaults to assigned configuration's latest ref, otherwise require selection/creation |
| `preparation_id` | `--preparation`; committed source for this host, matching any explicit configuration |
| `installer_id`, `builder_id` | Optional `--installer`, `--builder` choices |
| `task_id` | Optional `--task` resumes the existing task |
| `review_id`, `acknowledgements` | On resumption: current review and `{confirm_host?, typed_device_paths?}` |

Without a preparation, use shared onboarding forms. Pin chosen source before
building and revalidate observations before effects. Entry/media preparation
requires no assigned configuration.

Submission without confirmation is valid and cannot enter a destructive phase.
Task detail contains the current review. Interactive waiting displays it and
submits its ID with the prompted acknowledgement. Scripts resume with:

```sh
nixmox deploy install cedar --task task_01 --review review_01 --confirm-device /dev/disk/by-id/nvme-EXAMPLE --wait
```

The embedded review binds the task's intent. Boot reviews require `--confirm-host`;
erase reviews require every `--confirm-device`. Neither grants approval for the other.
Resume only the awaited review; changed effects require a new one. Resuming with
`--task` and no acknowledgement regenerates a stale review of the same pinned intent
and pauses, without changing source or repeating effects. Store verified acknowledgements
as bound `confirmation` evidence. Browser/TUI use the same resumptions with unique
command IDs; noninteractive waiting returns exit 4.

`host.enter_installer` takes `host_id`, `method` (CLI reboot uses `reboot`), optional
`installer_id`, `task_id`, `review_id`, and `acknowledgements: {confirm_host}` at entry
review. Standalone `hosts reboot --into installer` follows the same pause/resume
contract, accepting `--installer`, `--task`, and `--review`; initial submission does
not need `--confirm-host`. Scripts resume the exact entry review, for example:

```sh
nixmox hosts reboot cedar --into installer --task task_entry_01 --review review_entry_01 --confirm-host cedar --wait
```

Media-write reviews remain separate child tasks. Changed entry plans need renewed
approval; the ordinary `host.reboot` operation still requires direct host confirmation.
When already in a verified installer, skip entry and its boot approval. Otherwise
inspect entry compatibility, including media that can be prepared, before disruptive
actions. Compatible but unwritten media is a preparation step, not an unsupported host.

| Entry method | Requirement |
| --- | --- |
| Already in installer | Verified compatible agent/environment, including USB/SD or kexec boot; no transition required |
| kexec | Verified capability, sufficient memory, and a compatible Nixmox installer kernel/initrd |
| reboot | Compatible boot adapter and device containing a valid installer, or eligible to be formatted with one; prepare and verify it before boot selection |

`auto` prefers supported kexec; otherwise use a ready or preparable reboot route. Never assume
that an architecture guarantees kexec or a bootloader supports one-shot selection.
An explicit method fails clearly when unavailable. A failed/uncertain handoff is
reconciled, not retried by blindly switching methods. A running-OS target with
neither supported kexec nor a ready/preparable reboot route is **incompatible for installation**:
return `INSTALLATION_UNSUPPORTED` with the capability reasons before any effects.
Do not require manual media boot when the agent can prepare media and select it.
This restriction does not prevent ordinary management or adoption. USB/SD remains
an independent entry path: a node booted into a compatible installer is evaluated
in that new environment. These rules apply to every machine profile.

### Preparing reboot media

Install and Reboot into installer reuse media inspect/fetch/build/plan/write as
child tasks. Offer compatible attached devices, reusing a valid Nixmox installer
without formatting or writing a compatible image to an eligible spare device.
Compatibility includes board/profile boot support, adapter reachability, capacity,
and continued availability outside the subsequent installation erase targets.

Formatting requires a separate device-bound confirmation. Reject devices backing
the running root, boot/EFI files, swap, active storage stacks, or required Nixmox
state/handoff; unmounted alone does not mean disposable. Recheck before writing,
verify readback, then stage the host-specific handoff on preserved storage. Do not
erase the installed-system target until the same node reports a verified installer.

All interfaces show Select/reuse installer media → review SD/USB erase if needed
→ prepare media → approve reboot → select/create configuration → prepare/install.
CLI `nixmox deploy install cedar --method reboot --wait` guides these steps;
noninteractive execution pauses with the normal approval-required result. Record
the profile, device, media plan, and entry adapter in the parent task. The installer
status reports `state: "needs_media_preparation"` with `installation_supported: true`
when a compatible route exists but media still needs writing.

Use the shared Nixmox installer definition for both media and kexec, packaged as
disk images or a kernel/initrd payload respectively. Use `kexec-tools` to load the
profile's kernel/initrd and explicit installer command line, then `systemctl kexec`
for orderly service shutdown. Do not inherit the running system's root arguments
or force an immediate `kexec -e`. Verify platform/kernel support and loading before
shutdown; unload a task's staged payload on cancellation. Respect kernel lockdown
and signing requirements; unavailable kexec follows the existing method-selection rules.
[kexec-tools](https://github.com/horms/kexec-tools/blob/main/kexec/kexec.8),
[systemd kexec](https://www.freedesktop.org/software/systemd/man/latest/systemctl.html#kexec)
Do not use an environment missing the Nixmox agent. The SSH-bootstrap or running-OS
agent prepares the same protected handoff before entering the installer. Agent
identity and task checkpoints persist across bootstrap → installer → installed OS.
Preserve scoped TLS credentials/trust and required SSH/SOPS keys using the
[security state inventory](identity-and-trust.md#generated-and-preserved-state),
including issuer state when this node owns it.

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
  "entry_adapter": "rpi-eeprom",
  "installed_adapter": "rpi-uboot-sd",
  "state": "ready",
  "protected_device_ids": ["disk_sd"],
  "preserve_identity": true,
  "network_handoff": "ready"
}
```

Readiness requires verified image/boot files, boot selection support, and handoff
storage/network prerequisites; it is not proof that hardware has already booted it.
The bootloader adapter verifies/stages the entry, arms the next boot, and records
its receipt. Record consumption of one-shot selection on installer arrival.
Persistent selectors stay on the installer until an installed system is ready;
then select that system before reboot. Installer arrival never authorizes erasure.
Where one-shot selection is unavailable, use a tested persistent-selection/restore
adapter with explicit interrupted-boot recovery; otherwise that reboot method is
unsupported. If kexec is also unavailable, installation is incompatible for now.

### Reboot adapters

Profiles declare the adapter and boot layout; hardware model alone cannot select
one. Each adapter implements inspect, stage, select installer, select installed
system, and observe. Persist prior/desired selection and reconcile interrupted
changes; never blindly repeat firmware updates or destructive installation.
Track `entry_adapter` and `installed_adapter` separately: migration may change the
boot layout; the latter is null until selected through configuration/preparation.
After installation, use the installed adapter's boot selection, not a blind restore
of the old firmware order. Subsequent installer entry follows the new layout.

| Adapter | Boot layout and selection |
| --- | --- |
| `rpi-uboot-sd` | nixos-hardware + U-Boot on SD; select installer or installed entry in the SD extlinux configuration. Both entries load kernel/initrd/device trees from SD; installed Linux mounts NVMe root. |
| `rpi-eeprom` | Independent firmware-bootable SD installer and NVMe installed system; use raspberrypi-eeprom tools to change EEPROM `BOOT_ORDER`. NVMe needs its own boot files/partition, not just a root filesystem. |
| `uefi-bootnext` | Dedicated installer EFI executable on a preserved ESP; use `efibootmgr` to select its firmware entry for the next boot. |

For `rpi-uboot-sd`, own a profile-defined extlinux selection file and stage complete
entries before replacing it. Preserve installer assets and integrate with NixOS
boot-file generation so Apply cannot remove the installer or overwrite an armed
selection. Do not assume generic extlinux provides one-shot boot or U-Boot can read
NVMe on Pi 5. The SD installer root remains separate from NVMe erase targets.
[nixos-hardware boot layout](https://github.com/NixOS/nixos-hardware/blob/master/raspberry-pi/README.md)

For `rpi-eeprom`, read the existing configuration and preserve unrelated settings.
Example orders: `0xf61` = SD, NVMe, repeat; `0xf16` = NVMe, SD, repeat (right to left).
These are persistent preferences, not one-shot selection. Keep SD first during
installation; select NVMe first only after its boot files/system are ready. Use
pinned EEPROM tooling/image, retain the previous settings in the task receipt,
and observe the applied configuration after any required reboot before advancing.
Fallback to another device is not evidence that the intended installer booted.
[Raspberry Pi boot order](https://www.raspberrypi.com/documentation/computers/raspberry-pi.html#BOOT_ORDER)

For `uefi-bootnext`, require UEFI mode and writable EFI variables. Stage
a dedicated firmware-loadable installer image/loader, preserving normal BootOrder.
Select its resolved entry with `efibootmgr --bootnext 0007` (example ID), verify
BootNext, then reboot. A GRUB/systemd-boot menu entry alone is not an EFI Boot####
entry. Secure Boot requires an accepted signed loader/payload; do not disable it.
[efibootmgr](https://github.com/rhboot/efibootmgr/blob/main/README)

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

## V1 deployment scope

Deployments target one host. Multi-host rollouts, canaries, and automatic rollout
rollback are future work; no rollout command is exposed in v1. Host and cluster
[health status](host-inventory-and-lifecycle.md#health-status) is included in v1.
After activation, refresh and display health alongside exact running-output
verification. Report degraded health separately; it does not trigger automatic rollback.
