# Host onboarding

Onboarding establishes an agent, collects hardware, and prepares a configuration.
`deployment.install` owns the installation task and calls the preparation,
confirmation, and installation services specified here. GitOps owns the resulting
commits/artifacts; it does not own hardware collection or destructive confirmation.

## Entry paths

| Starting point | Workflow |
| --- | --- |
| No OS | Boot USB/SD installer agent → network setup → join pending → prepare → Install |
| Kexec entry | Running agent → shared Nixmox kexec installer → resume same host/task → Install |
| Installed Nixmox | Running-system agent → verify/adopt or Apply; fresh Install requires supported installer entry |
| SSH-accessible Linux | Temporarily bootstrap agent → join pending → inspect → Apply/adopt supported NixOS or enter installer for Install |

All paths converge on an authenticated agent. Agent presence alone grants neither
full membership nor installation compatibility. An ordinary OS agent must enter
a verified installer before erasing its running system. No supported kexec/reboot
route means installation is incompatible for now; there is no further fallback.
A node already booted from USB/SD into the installer needs no entry transition.

### SSH agent bootstrap

Use SSH only to inspect OS/architecture/privileges, transfer a verified compatible
agent bundle, and start a temporary service. Do not require Nix or NixOS merely to
start it, or install a permanent management stack into an OS about to be erased.
Support explicitly tested Linux environments; unsupported OS, privileges, or agent
runtime return a specific error. Stage any additional transition-tool dependencies
only when needed; agent startup itself does not require them. The agent exposes
the normal console and limited
bootstrap capabilities, enrolls as pending, and uses NATS for subsequent operations.
SSH does not become a parallel host-management workflow.

```sh
nixmox hosts bootstrap --ssh root@192.168.1.32 --credential bootstrap-ssh --wait
```

Browser/TUI: Hosts → Add via SSH → address + Nixmox credential → verify host key
→ Start agent → pending host detail. `host.bootstrap` takes `ssh_target`,
`credential_ref`, and confirmed `host_key_fingerprint`; interactive CLI performs
the same verification. Scripts supply `--host-key-fingerprint`. Credentials come
from Nixmox's protected store, not a node's ambient SSH agent, and never enter task
payloads. A credential-capable executor performs bootstrap and hands over a scoped
enrollment token; completion requires observing the authenticated agent connection.

Reuse a verified existing Nixmox identity/service instead of starting a competing
agent. Otherwise generate identity on the target and preserve it through installer
entry and final installation. Track temporary service/files for cleanup on cancel,
expiry, or handoff; keep them alive while an admitted operation needs them. Successful
Apply, verified adoption, or installation provides the permanent NixOS-managed
service. Adoption requires that service/configuration already be present; Apply
can configure it on a running NixOS target without formatting. Retried
bootstrap resumes its receipt rather than creating a second host identity.

## Bootable media workflow

Use [installer profiles](installer-images.md) for x86 PCs, Raspberry Pi 5, and
custom hardware. A profile selects architecture, boot support, and image format;
USB and SD writing share the same flow.
Build on the writing node by default when capable; another builder is a fallback
or explicit override. SD cards in built-in and USB readers are supported targets.

```sh
nixmox media build --profile pc-x86_64 --host alder --wait
nixmox media devices --host alder
nixmox media plan --image media_01 --host alder --device /dev/disk/by-id/usb-EXAMPLE --wait
nixmox media write --plan media_plan_01 --confirm-device /dev/disk/by-id/usb-EXAMPLE --wait
```

`media.plan` captures image digest, observed device identity, size, and eligibility.
`media.write` verifies that plan again, writes, then performs readback verification.
The device must be attached to the writing node, which need not serve the console.
A mount, identity change, or conflicting task invalidates the plan. Never select
an arbitrary disk automatically or embed shared machine credentials in the image.

Browser: Media → Create installer → profile + node alder/device → Build → review
model/serial/capacity → type exact device → Write. TUI follows the same Media screens
and requires the same typed confirmation. A task shows build/write/readback phases,
plus transfer when another node built the image.

## Installer startup and network setup

Bootable media starts the Nixmox daemon and launches `nixmox tui --installer` on
the local console automatically. Installer mode uses the same UI and services,
with network setup and onboarding as its starting screens. Neither a keyboard nor
a display is required for the daemon, discovery, or browser gateway to start.

1. Attempt wired DHCP and check internet access from the installer itself. Report
   link, address, DNS, and internet status separately; an address alone is not
   proof of internet access. A failed/expired probe reports offline or unknown,
   rather than leaving an endless spinner.
2. If internet is unavailable, show: “Not connected to the internet. Plug in a
   wired connection, or connect to Wi-Fi.” Show Retry and, only when a supported
   Wi-Fi adapter is detected, Connect to Wi-Fi. Update automatically when a cable
   is connected or an adapter appears.
3. Wi-Fi opens a network dropdown with SSID, signal strength, and security. Select
   a network, enter its password in a masked field, and Connect. Open networks
   need no password. Wrong passwords, unavailable adapters, unsupported security,
   and DHCP failures remain actionable in this screen; Rescan refreshes the list.
4. Once internet is verified, open **Clusters**. Keep connectivity status visible;
   losing connectivity does not reset enrollment or installation state.

The check measures the target's connectivity, never the browser device's. Local
discovery and console access start as soon as a usable LAN address exists, even
if internet is unavailable. Show that distinction and allow Continue on local
network; Git fetch/build tasks report their actual missing network prerequisites.
Internet probe failure must not prevent an existing cluster from adding the node.

```json
{
  "node_id": "n_cedar",
  "environment": "installer",
  "network": {
    "interface": "wlan0", "link": "up", "address": "192.168.1.32",
    "dns": "ready", "internet": "online",
    "checked_at": "2026-10-20T12:00:00Z"
  },
  "wifi_networks": [
    {"id": "wifi_01", "ssid": "Homelab", "signal_percent": 82, "security": "wpa2-personal"}
  ],
  "console_url": "http://192.168.1.32:63170",
  "next_screen": "clusters"
}
```

Wi-Fi passwords stay in the installer's local network credential store, never in
Git, task payloads, JetStream, logs, or telemetry. Use protected secret ingress
for browser submission and local ingress for TUI/CLI, without requiring Git/SOPS
setup. Network handoff preparation must explicitly arrange installed-system
connectivity; a working live-media Wi-Fi connection is not proof of that handoff.

```sh
nixmox network status
nixmox network check --wait
nixmox network wifi scan --wait
nixmox network wifi connect --network wifi_01 --wait
# Connect prompts for the password through hidden input, never a command argument.
nixmox tui --installer
```

Reads: `/network/status`, `/network/wifi/networks`. Commands: `network.check`,
`network.wifi.scan`, `network.wifi.connect` (network ID and optional local
credential reference). `POST /api/v1/network/wifi/credentials` stages a password
using the same redaction/protected-transport rules as credential import and returns
an opaque, short-lived reference. No cluster membership or quorum is required for
these node-local bootstrap operations. All interfaces use the same status/events.
**Decision required:** configurable internet probe endpoint(s) and captive-portal
detection. Probe failures must remain distinct from loss of the local network.

## Clusters, joining, and headless setup

The **Clusters** screen is shared by installer and installed unjoined nodes in
browser and TUI. Stream discovered cluster names, IDs, and reachable gateways into
the list while showing “Searching for clusters…”. After 30 seconds without a result,
show “No clusters found” with Search again. Continue listening for late results;
Search again sends a fresh discovery request and restarts the visible timeout.
Expire stale entries and recheck identity/reachability before joining. Manual
address entry and Create cluster remain available when broadcasts cannot reach a
cluster or this is the first node.

CLI Search again: `nixmox discovery search --wait` (`discovery.search` command);
`nixmox discovery list` reads the live results. The search task completes after its
30-second window, even with no results; passive discovery continues independently.

Select a cluster → show cluster name/ID and gateway → Confirm joining → pending
membership → select an existing configuration or Create configuration. Creation
uses the shared generation forms, then returns to target preparation. Configuration
assignment alone never grants full membership. Installed nodes use deployment or
verified adoption; installer nodes proceed through preparation and installation.
Configured address/name autojoin still works and resumes at configuration setup.

```text
USB boot → installer TUI → network setup → Clusters → confirm join
                          ↑                  ↑              ↓
                    browser at IP:63170 ─────┘     pending → configuration → prepare/install
Existing cluster → Discovery → installer → Add ─────┘
```

Ethernet DHCP automatically enables the media's management interface, browser
gateway, and discovery; no local TUI confirmation is needed. Show current console
URLs on the TUI and in discovery records. A headless host can be configured at
`http://<installer-ip>:63170`, or through an existing cluster's Discovery → node
→ Add. Browser and TUI reflect the same persisted enrollment/task state; joining
from either side advances all open consoles and does not enroll the node twice.
Concurrent attempts to join different clusters must conflict, never overwrite an
accepted association.
Address changes update advertised URLs while preserving node identity. A browser
session that loses its address shows reconnect guidance rather than restarting work.

## Observation and preparation objects

The target collects observations; the client cannot submit invented disk inventory
as authoritative evidence. An observation expires after five minutes by default,
and destructive actions always re-observe regardless of age.

```json
{
  "schema_version": 1,
  "id": "obs_01",
  "host_id": "n_cedar",
  "observed_at": "2026-10-20T12:00:00Z",
  "system": "x86_64-linux",
  "boot_mode": "uefi",
  "hardware_digest": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  "disks": [{
    "id": "disk_01", "path": "/dev/disk/by-id/nvme-EXAMPLE",
    "model": "Example NVMe", "serial": "EXAMPLE", "size_bytes": 512000000000,
    "eligible": true, "ineligible_reasons": []
  }, {
    "id": "disk_02", "path": "/dev/disk/by-id/ata-DATA",
    "model": "Example data disk", "serial": "DATA", "size_bytes": 2000000000000,
    "eligible": true, "ineligible_reasons": []
  }],
  "public_identity": {"node_id": "n_cedar", "age_recipient": null},
  "network": {"active_interfaces": ["enp1s0"], "handoff_ready": true}
}
```

Hardware data includes boot-relevant drivers/platform information, not secrets.
Disk eligibility excludes installer media, mounted/active parents, read-only devices,
and unsupported layouts. Retries after partial installation need an explicit
reconciliation rule for mounts created by that task, not a blanket busy-disk bypass.

```json
{
  "schema_version": 1,
  "id": "prep_01",
  "version": 3,
  "host_id": "n_cedar",
  "configuration_id": "cfg_cedar",
  "observation_id": "obs_01",
  "template_values": {"disk": {"SystemDisk": "disk_01"}},
  "templates": {
    "hardware": "linux-hardware",
    "disk": "existing-layout",
    "repository_commit": "0000000000000000000000000000000000000001"
  },
  "sops": {"updater_ref": null, "state": "skipped", "reason": "no_updater_configured"},
  "state": "reviewable",
  "draft_id": "draft_prepare_01",
  "source_commit": null,
  "confirmation": null
}
```

## Prepare commit install

```sh
nixmox cluster add --discovery discovery_cedar --wait
nixmox hosts assign cedar --repository lab --configuration cedar --wait
nixmox onboarding inspect cedar --wait
nixmox onboarding prepare cedar --configuration cfg_cedar --observation obs_01 --disk-template existing-layout --set disk.SystemDisk=disk_01 --wait
nixmox onboarding show prep_01 --diff
nixmox onboarding commit prep_01 --expected-version 3 --wait
nixmox deploy install cedar --preparation prep_01 --builder birch --confirm-device /dev/disk/by-id/nvme-EXAMPLE --wait
```

Preparation collects hardware, disk choices, and optional SOPS updates together.
Commit hands the reviewed diff to GitOps, pins the accepted commit, and replicates
it. No remote push is required. Installation checks target identity, config export,
Nixmox enablement, network handoff, and evaluated disk layout before effects.
Build on birch and install on cedar; source/output transfer never requires giving
the installer administrative repository credentials.
Build/download and target-transfer stages must expose the same structured
[progress feedback](deployment-and-rollback.md#required-build-and-deployment-progress)
as ordinary deployments, including cache sources, rates, active builds, and bars.

Preparation uses the configuration's compatible hardware/disk template lists.
Auto-select each stage when it has one choice; with multiple choices, show a
selector or accept `--hardware-template` / `--disk-template`. Thus the example
auto-selects `linux-hardware` and explicitly selects the binding-only disk template.
To generate a layout instead, select `--disk-template ext4-layout`; the preparation
diff includes pinned layout assets and the rendered device binding.

Both stages use the output paths and formatting defined by the repository.
See [dedicated templates](configuration-generation.md#dedicated-hardware-template)
for manifests and renderers. Disk selectors are ordinary `type: "disk"` form fields;
their values are observed IDs, resolved to device records by the service.
No configured disk renderer means the user must provide one or explicitly use an
already complete layout whose evaluated devices match the selected disks; do not
silently choose a partitioning scheme.

| Stage | Browser | TUI |
| --- | --- | --- |
| Connect | Discovery → cedar → Add | Discovery → cedar → Add |
| Bind | cedar → Set up → select lab/cedar or Create | Hosts → cedar → Set up → same selector |
| Prepare | Hardware template/form → disk template and its device fields → optional Secrets; skip sole-template selectors | Preparation tabs: Hardware / Disks / Secrets, with the same selection rules |
| Commit | Combined diff → Commit locally | Review diff → Commit locally |
| Install | Builder birch → disk review → typed confirmation | Install review → same builder/confirmation |
| Finish | Task → waiting for reboot → verified full member | Task → Result, then host membership badge |

If the configuration is created during this flow, return to preparation after the
standalone generation flow; do not duplicate its forms or validation logic.

## Confirmation and checkpoints

Confirmation binds preparation version, node ID, observation digest, selected disk
identities, candidate/source, and expiry. Typing a path is an acknowledgement,
not sufficient evidence by itself. Any changed binding invalidates confirmation.

```json
{
  "preparation_id": "prep_01",
  "preparation_version": 4,
  "host_id": "n_cedar",
  "source_commit": "1111111111111111111111111111111111111111",
  "observation_id": "obs_01",
  "disk_ids": ["disk_01"],
  "typed_device_paths": ["/dev/disk/by-id/nvme-EXAMPLE"],
  "expires_at": "2026-10-20T12:05:00Z"
}
```

The service issues the review binding/expiry and the client supplies typed
acknowledgement; the target verifies both against its recorded preparation and
fresh observations. The client cannot extend expiry by editing JSON.

```text
Set up cedar                      Pending • installer
Configuration   lab / cedar       Local commit 1111111
Builder         birch             x86_64-linux
System disk     Example NVMe       512 GB • serial EXAMPLE
Secrets         Skipped            No updater configured
Network         LAN → tailscale0   Handoff must be verified

[Review changes]  [Commit locally]  [Review installation]
```

Browser panels and the TUI detail screen show these same values. Install stays
disabled with a specific reason until preparation is ready and the reviewed commit
is available to the chosen executor.

```text
inspect → prepare → review → commit → confirm
                                      ↓
verify → build/transfer → partition → install → preserve identity/network → reboot
                                                                           ↓
                                                       observe same ID/output → full
```

Persist effect intents/checkpoints. Loss of connection after partition/install
enters reconciliation. A completed installation proceeds to reboot/observation,
never repeats partitioning. A partial destructive retry requires a new observation,
review, and confirmation. Preserve logs and original source revision.
Re-running preparation later generates a new diff/commit; it does not reinstall.

## Installation backend and handoff

[Install](deployment-and-rollback.md#reboot-into-installer-and-fresh-install)
uses this flow for both first installation and replacement of an existing system,
after Nixmox has entered and authenticated the installer. Use a
pinned **nixos-anywhere** adapter for partition/install execution; Nixmox owns
boot selection, source/candidate pinning, confirmations, progress, and recovery.
Its phase selection permits skipping kexec/reboot, and prebuilt store paths avoid
rebuilding a different configuration inside the adapter.
[nixos-anywhere reference](https://github.com/nix-community/nixos-anywhere/blob/main/docs/reference.md)

Run the adapter on the installer against a task-scoped, loopback-only SSH endpoint,
with ephemeral credentials and host-key verification. Cluster orchestration remains
over NATS; users need no SSH setup for enrolled nodes. This internal transport is
separate from the initial SSH bootstrap described above. Verified build outputs can originate on
another builder and must be available before destructive execution.

Backend shape (service-provided paths/credentials, not user-run CLI commands):

```sh
nixos-anywhere --target-host root@127.0.0.1 --ssh-port "$installer_port" \
  -i "$task_key" --ssh-option "UserKnownHostsFile=$task_known_hosts" \
  --store-paths "$disko_closure" "$system_closure" --phases disko
# Nixmox verifies the result, then mounts preserved boot partitions under /mnt.
nixos-anywhere --target-host root@127.0.0.1 --ssh-port "$installer_port" \
  -i "$task_key" --ssh-option "UserKnownHostsFile=$task_known_hosts" \
  --store-paths "$disko_closure" "$system_closure" \
  --extra-files "$handoff_directory" --phases install
```

Phase receipts, actual filesystems, firmware population, and boot-entry verification
are mandatory before selecting the installed system and rebooting. A shell exit
code alone cannot establish readiness. Test the pinned adapter's phase boundaries;
its default all-phases invocation must never bypass these checkpoints.

Handoff is common to every installation: preserve Nixmox node identity, scoped
enrollment/recovery evidence, and transferable network settings. Copy active Wi-Fi
credentials locally with restrictive permissions when the target supports that
network backend; otherwise require a working alternative before entry. Keep values
out of Git, JetStream, logs, and traces; SOPS setup is not required. Stage the minimal
handoff outside erase targets and remove consumed transient copies. Preserve any
required host/decryption keys explicitly, not the entire old root or credential store.

For Pi SD boot + NVMe root, preparation separates **erase NVMe** from **preserve
SD partitions/update boot files**. Verify observed partition identifiers, mount the
installed `/boot` and `/boot/firmware` on SD, and leave the SD installer root intact.
The preserved partition selectors and boot-update authorization are distinct from
Disko's destructive device set. The installed configuration references stable IDs.
Do not rewrite the SD partition table or format a live installer filesystem.
Updates to shared boot files require a profile-tested staging/recovery protocol.

## Optional SOPS contract

No updater configured: show Skipped and leave secret files unchanged. This must
not block configurations that need no secrets. If required access is unknown,
show that prerequisite rather than claiming decryption will work.

Proposed updater interface accepts repository worktree ID, node public recipient,
allowed file paths, and intended recipient changes. It returns changed encrypted
files and a structured result; it never returns private keys or plaintext secrets.
Only a configured credential-capable replica executes it. Requested update failure
blocks commit of the combined preparation until resolved or explicitly deselected.

**Decision required:** built-in SOPS writer versus external integration; recipient
identity choice/preservation; rule editing support; how readiness is verified.
A configured method enables the feature; it does not make unrestricted file edits
or automatically grant every host every secret.

## API and acceptance

Commands: `host.bootstrap`, `media.build`, `media.plan`, `media.write`,
`onboarding.inspect`, `onboarding.prepare`, `onboarding.commit`. Installation uses
the single `deployment.install` command defined in the deployment spec. Payloads
match the CLI selections above; commit uses expected preparation/repository versions,
and installation includes the bound confirmation. Reads: `/media/{id}`,
`/hosts/{id}/disks`, `/observations/{id}`, `/preparations/{id}` and its `/diff`.

`onboarding.prepare` accepts optional `hardware_template_id` and `disk_template_id`,
typed `template_values` grouped by `hardware` and `disk`, plus observation ID. Disk
fields contain selected disk IDs, not client-supplied device facts. Resolve
omitted IDs using the single-template rule and persist both IDs and source revision
in preparation. API callers cannot replace service-collected hardware or disk facts
with template values.

Acceptance: USB write/readback; swapped disk rejection; no-host configuration reuse;
no-SOPS install; failed optional updater; install using remote builder and unpublished
commit; reboot on a different interface with the same ID; interruption after disk
effects; fresh preparation after hardware changes without accidental installation.
Also test template auto-selection, disk-layout generation versus binding-only,
and confirmation of every selected device in a multi-disk form. CLI installation
accepts one `--confirm-device` per selected device; omission is an error.
Also test automatic installer TUI startup, wired headless setup, absent/hotplugged
Wi-Fi adapters, scan/password errors, internet versus LAN-only status, discovery
timeout/retry/late results, joining from either side, and browser/TUI state parity.
SSH bootstrap acceptance: supported Linux without Nix, insufficient privilege,
host-key mismatch, duplicate request, cleanup, agent handoff over NATS, and preserved
identity across boot. Reject installation-incompatible hosts before boot/erase.
