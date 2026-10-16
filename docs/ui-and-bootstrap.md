# Live operations, system previews and USB bootstrap

## Design decisions

The organizing principle is to answer three separate questions: **is the host reachable, does its revision need attention, and what will applying change?** Argo CD explicitly separates target state, live state, sync-operation status and health ([core concepts](https://argo-cd.readthedocs.io/en/stable/core_concepts/)). Nixmox follows that separation without claiming that a matching commit proves runtime health.

| Choice | Why and how it helps |
| --- | --- |
| Revision status in the host inventory | `Repository update`, `Needs apply`, `Never applied`, and `Apply failed` expose actionable differences without opening every node. Available and pulled SHAs remain separate: pulling does not activate a system. Offline hosts show unknown status rather than a reassuring stale result. |
| Recorded apply mode and kind | A boot-mode apply is labeled staged for boot; dry activation and rollback do not masquerade as normal deployment success. Old records remain readable and conservatively display `Revision recorded`. The label uses the recorded operation, not today's configured apply mode. |
| Host-first review flow | Pull, preview and apply sit together; tasks appear immediately below. Hardware/telemetry and power controls remain available in a collapsible section, so large disk/interface inventories do not push current work offscreen. |
| Evaluated system preview | A queued `preview` builds the same revision-pinned flake used by apply, with local impure inputs, without running activation. It compares `/run/current-system` with the candidate using `nix store diff-closures` and a recursive unified diff of their generated `etc` trees, including systemd units. This reveals actual package versions and configuration changes, not just Git filenames. |
| Durable, separately readable preview sections | Package and configuration output live in the completed job record, separately from the scrolling command log. Previews are bounded to 256 KiB + 512 KiB, with explicit truncation warnings and a complete-log download. A long configuration diff cannot silently push package information out of the log tail. |
| Honest progress | Apply/build operations show an indeterminate bar, phase, live output, errors and terminal state. There is no invented build percentage. USB writing and verification show measured MiB progress; rollouts show completed hosts out of total hosts. Non-apply tasks display `completed`, retaining the compatible underlying job state. |
| Scoped task history | The global Tasks page aggregates reachable hosts with bounded concurrency and explicit unavailable-host warnings. Its host selector changes the live subscription. Each host page embeds its own tasks; logs and downloads route to that host through the existing one-hop forwarding rule. |
| More resilient live fragments | A slow remote fragment no longer serially blocks all others. Reads run concurrently within the existing eight-subscription limit and a three-second batch deadline. Failed fragments replace stale content with a retry message; recovery resends fresh content. Log scroll position is preserved, with tail-follow only when already at the bottom. |
| Responsive layout | Revision summaries collapse to one column, task tables scroll within their container, actions wrap, and custom-command controls are folded away. Desktop and 390px layouts were checked in Chromium. |
| Discovery during setup | New nodes can select a discovered cluster without manually transcribing its address. Operations refreshes discovery and USB inventory every ten seconds while visible. A discovered host can prefill the installation SSH target, while destructive confirmation stays explicit. |

## Preview workflow and limits

1. Open a host, then **Pull latest revision**. Wait for its task to complete.
2. Select **Preview packages & configuration**, then follow **Watch progress**.
3. Review the candidate SHA, package closure changes and generated configuration diff.
4. Return to the host and apply. Follow the host task's phase and log.

Preview builds can download or compile packages and consume disk space. They share the host's FIFO queue, so an apply cannot run concurrently through the same agent. The temporary preview GC root is removed afterwards; a later apply may need to realize the candidate again. Running NixOS is required for the baseline.

This is a preview of a specific candidate against the running system, not a complete simulator for arbitrary activation scripts, service health, secrets or externally managed state. It is not an approval lock: pull changes, local-input changes or external rebuilds can invalidate it. Re-run preview before applying after any such change. A boot-mode record remains labeled staged until another operation; nixmox does not currently prove reboot completion. Git revision status cannot detect changes to local inputs or manual edits by itself.

The package comparison uses Nix's native [closure diff](https://nix.dev/manual/nix/latest/command-ref/new-cli/nix3-store-diff-closures.html), including transitive dependencies. Same-version store-path changes smaller than Nix's reporting threshold may not appear in that summary. The generated `etc` diff provides configuration detail; it is not a list of guaranteed service restarts. The complete task log is available from the task page. Logs may contain generated configuration, so keep the existing trusted-management-network boundary.

## USB installer workflow

Enable USB operations on the node physically connected to the drive:

```nix
services.nixmox = {
  enableMedia = true;
  # Default: github:Coriago/nixmox#installer-iso.
  # Pin the reviewed revision in production, or select your local checkout:
  installerFlake = "path:/path/to/nixmox#installer-iso";
};
```

Equivalent YAML keys are `enable_media` and `installer_flake`; environment variables are `NIXMOX_ENABLE_MEDIA` and `NIXMOX_INSTALLER_FLAKE`. The service module provides `nix`, `diffutils`, and, when media operations are enabled, `util-linux`. Writing requires root/block-device privileges; the system service already runs as root.

1. Open **Operations → Add a host · USB installer** on that node's portal. Drives belong to the serving node, not the browser's computer.
2. Optionally paste one SSH **public** key matching the controller's SSH identity. This is needed for remote installation into the new machine's internal disks. Click **Prepare installer image** and follow its task. No private key is copied to the image. With a key supplied, the configured source must export `lib.mkInstaller`; the package fragment is replaced by that builder to produce a customized x86_64 ISO. Without a key, the configured package output is built unchanged.
3. Select the USB drive by path, model, serial and capacity. Unmount its partitions first if marked busy. Type the exact device path, then confirm **Erase and write installer**.
4. Watch the write, flush and readback-verification phases. Wait for the task to complete before removing the drive. A failure or interruption can leave incomplete media; repeat the write after checking the device.
5. Boot the new computer. The live image provides DHCP, a unique hostname, discovery and nixmox. It is a temporary environment. Use **Prepare installation** from discovery to fill its SSH target; provide a reviewed flake with disko, hardware configuration, nixmox, and optionally `services.nixmox.joinSeed`. Install to the internal disk, then join the installed host through discovery or its configured seed. Joining the live environment alone is not a persistent installation.

The image uses the existing NixOS minimal **x86_64 hybrid ISO**, with USB and EFI boot support. Write the whole image to the whole device; do not create a filesystem first. This follows the [NixOS installation manual's USB-image approach](https://nixos.org/manual/nixos/stable/#sec-installation-booting). The UI deliberately does not offer the generic aarch64 SD output: board bootloaders vary, so presenting it as universal USB media would be misleading. Secure Boot setup and firmware-specific boot behavior are outside this writer.

### Drive admission and failure behavior

- Only whole USB disks with capacity and a serial number appear as eligible. Internal SATA/NVMe disks and partitions are excluded. USB disks with `RM=false` may still qualify: many USB bridges report that flag inaccurately; transport plus explicit identity/confirmation is used instead.
- Read-only media, mounted descendants, active swap and mapped descendants are rejected. `lsblk --tree` is intentional: without it, selecting explicit columns omitting `NAME` can flatten the output and hide mounted descendants.
- A fingerprint binds path, device number, model, serial and capacity. Admission checks it again when the queued job starts, and after opening the device. A stale selection does not silently target a replacement drive.
- Linux exclusive block-device opening adds a kernel-level busy check. The writer never automatically unmounts, creates or truncates a regular file, or selects a destination implicitly.
- The job pins the prepared image's Nix-store path, checks its size against the drive, writes bytes, synchronizes, invalidates the block-device cache, and hashes the readback. Verification covers the image-sized prefix; it does not securely erase the remaining drive capacity.
- Only one installer operation can be queued/running through this agent. A write cannot be admitted while a replacement image is building. Pending USB writes are failed on restart and require fresh user confirmation; interrupted writes are never resumed automatically.
- The server checks `enableMedia` both when admitting and executing a task. The existing same-origin checks apply. These are accidental-erasure controls, not authentication; the HTTP service still requires a trusted management network.

## Refactoring and compatibility

The shared task-row template is used for host and global history. Media HTTP handlers are small named functions; disk discovery, selection, copying/verifying and installer arguments live in `internal/media`, independently testable without touching hardware. Preview reuses the existing pinned-reference builder, queue, process cancellation, logs and job persistence. No frontend framework or build pipeline was added.

Job phases, progress and preview fields; apply-record mode/kind; and the available revision in host heartbeats are additive JSON fields. Existing persisted state remains readable. Job-list responses omit large preview bodies; individual job endpoints retain them. Existing local task/action routes remain available. Global list routes now aggregate peers and expose partial failures in HTML or `X-Nixmox-Task-Warning` headers.

## Verification

- Go suite with race detection and `go vet`.
- Unit tests for revision states, non-activating previews, bounded output, USB eligibility/mounted descendants, stale identities, copy/readback, corruption, cancellation, key validation, and restart behavior.
- HTTP tests for host/global task scope and media admission gates.
- NixOS `gitops-apply` VM check extended to build and inspect a real candidate preview, verify the running system is unchanged, then apply and roll back.
- Chromium desktop and 390px checks for live SSE, scoped task history, page rendering, USB enumeration and horizontal overflow.
- Disposable QEMU USB check covers mounted-device rejection, actual exclusive block-device writing, cache flush, readback verification and byte-for-byte comparison without touching physical disks.
- The complete x86_64 installer ISO builds successfully; the portal, job-runner, GitOps apply and virtual USB NixOS checks pass.
- Installer evaluation confirms `makeEfiBootable = true`, `makeUsbBootable = true`, and discovery enabled.

No physical drive was overwritten during development. Physical USB boot and installation on other machines remain hardware acceptance checks.

## Live host inventory and navigation

The hosts table, host sync panel and cluster sidebar share status and action
rendering. Every page subscribes to the sidebar, which lists every inventory
member and marks the node serving the browser as **This UI**. Green checkmarks
mean the revision was applied; arrows mark updates/review, an exclamation mark
marks failed application, and a spinner identifies queued/running deployment.
Deployment phases link to the task; the table and host page include progress.
Motion respects the browser's reduced-motion preference.

Host inventory and deployment snapshots now travel through NATS JetStream KV.
Persisted job/log changes and Git changes wake publication immediately; a 250ms
coalescing window bounds bursts of build output. KV watchers wake the serving
node's SSE subscribers. The 10-second heartbeat remains for liveness, initial snapshots,
and recovery, and a 10-second SSE safety refresh covers TTL expiry and transient
failures. The browser receives an initial snapshot again after reconnecting.
There is no per-host HTTP status polling. See [the communication design](communications.md)
for durable command delivery, request/reply reads, and failure semantics.

Status and actions use the same rules: **Apply update**, **Retry apply**, or
**Reapply** for an already applied revision. A remote revision not pulled yet
says **Pull update first** and disables apply, avoiding accidental reapplication
of the older checkout. Revision status remains distinct from runtime health and
local-input drift. The browser talks only to the nixmox HTTP/SSE gateway; NATS's
client listener stays on loopback.

The comprehensive E2E now opens an SSE connection before remote apply and requires
subsequent deployment updates in both hosts and sidebar on that same connection.
It verifies all three sidebar hosts, the serving-node marker, progress/spinner,
and the transition to Reapply after activation. Unit/HTTP tests cover state
precedence, watched deployment changes overriding older state, and offline peers.

### Predictable freshness and lost connections

CPU, memory, disk I/O and network bandwidth now have a dedicated two-second
sampler and NATS Core feed;
it no longer waits for the 10-second membership heartbeat or depends on how busy
a deployment is. NICs, drives, mounts and local capacity refresh every 30
seconds, reducing repeated inventory work. Rates are derived from counters and
handle resets explicitly. Task logs stream incremental bytes with offset-based
recovery instead of replacing the whole log view. Uptime uses a one-second browser clock synchronized to actual
host reports. Offline hosts freeze their last value and reboot reports reset it.
This keeps the display steady while retaining event-driven task responsiveness.

A prominent disconnected banner replaces an easily missed header-only indicator.
It distinguishes stale displayed data from a live view, reconnects automatically,
and offers an immediate retry plus links to other previously online consoles.
Advertised peer addresses may be LAN-only, so changing browser origins remains
an explicit choice. A stream watchdog catches silent stalls as well as browser
connection errors. The comprehensive browser checks exercise these behaviors.
