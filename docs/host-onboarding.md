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
route, including one prepared on eligible attached media, means installation is
incompatible for now; there is no further fallback.
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

A temporary pending agent can inspect devices and execute explicitly authorized
media-write and boot-entry tasks on its own host. It need not become a full member
or adopt the old configuration first. Stage required tools with those capabilities;
media erasure still requires the normal plan and device confirmation.

```sh
nixmox hosts bootstrap --ssh root@192.168.1.32 --credential bootstrap-ssh --wait
```

Browser/TUI: Hosts → Add via SSH → address + Nixmox credential → verify host key
→ Start agent → pending host detail. `host.bootstrap` takes `ssh_target`,
`credential_ref`, and confirmed `host_key_fingerprint`; interactive CLI performs
the same verification. Scripts supply `--host-key-fingerprint`. Credentials come
from Nixmox's protected store, not a node's ambient SSH agent, and never enter task
payloads. A credential-capable executor observes the generated public key over
verified SSH and performs cluster-side Add bound to that key. No enrollment token
is seeded; completion requires observing the authenticated agent connection.

Reuse a verified existing Nixmox identity/service instead of starting a competing
agent. Otherwise generate identity on the target and preserve it through installer
entry and final installation. Track temporary service/files for cleanup on cancel,
expiry, or handoff; keep them alive while an admitted operation needs them. Successful
Apply, verified adoption, or installation provides the permanent NixOS-managed
service. Adoption requires that service/configuration already be present; Apply
can configure it on a running NixOS target without formatting. Retried
bootstrap resumes its receipt rather than creating a second host identity.

## Bootable media workflow

Build/fetch, plan, confirm, write, and read back media through
[installer images](installer-images.md#build-and-write). Reboot installation can
run these tasks on a pending bootstrap agent. Reject media required by the running
system; stage identity handoff only after writing.

### Example: Pi NVMe boot to SD U-Boot / NVMe root

1. Bootstrap the NVMe-running Pi over SSH and join pending.
2. Reuse a compatible SD installer, or confirm and write one to the unused SD card.
3. Preserve identity/network handoff; use `rpi-eeprom` to select SD and reboot.
4. In the installer, select/create a configuration with nixos-hardware templates:
   boot files on SD, root on NVMe, installed adapter `rpi-uboot-sd`.
5. Confirm NVMe erasure; install using cluster-cache outputs where available.
   Preserve the SD installer and write the installed boot files/extlinux entry on SD.
6. Keep EEPROM SD-first; select the installed U-Boot entry and reboot. Linux mounts
   NVMe root, and the permanent agent reconnects under the same identity.

## Installer startup and network setup

Bootable media starts the Nixmox daemon and launches `nixmox tui --installer` on
the local console automatically. Installer mode uses the same UI and services,
with network setup and onboarding as its starting screens. Neither a keyboard nor
a display is required for the daemon, discovery, or browser gateway to start.

1. Attempt wired address configuration and observe link/address state on eligible
   management interfaces. An active connection with a usable IPv4 or IPv6 address
   is ready; loopback and unrelated interfaces do not qualify. While acquiring an
   address, show “Obtaining network address…”; on timeout offer Retry/network settings.
2. If no connection is active, show: “Not connected to a network. Plug in a
   wired connection, or connect to Wi-Fi.” Show Retry and, only when a supported
   Wi-Fi adapter is detected, Connect to Wi-Fi. Update automatically when a cable
   is connected or an adapter appears.
3. Wi-Fi opens a network dropdown with SSID, signal strength, and security. Select
   a network, enter its password in a masked field, and Connect. Open networks
   need no password. Wrong passwords, unavailable adapters, unsupported security,
   and DHCP failures remain actionable in this screen; Rescan refreshes the list.
4. Once the connection is ready, open **Clusters**. Keep connection status visible;
   losing connectivity does not reset enrollment or installation state.

Observe the target's network state, never the browser device's. Discovery and
console access start when the connection is ready. No internet/DNS reachability
probe or captive-portal detection is performed; Git/cache tasks report their own
connection failures when needed.

```json
{
  "node_id": "n_cedar",
  "environment": "installer",
  "network": {
    "interface": "wlan0", "link": "up", "address": "192.168.1.32",
    "status": "ready",
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
Git, task payloads, JetStream, logs, or telemetry. All three interfaces submit them
through the installer's HTTP secret ingress, without requiring certificates or
Git/SOPS setup. Network handoff preparation must explicitly arrange installed-system
connectivity; a working live-media Wi-Fi connection is not proof of that handoff.

```sh
nixmox network status
nixmox network check --wait
nixmox network wifi scan --wait
nixmox network wifi connect --network wifi_01 --wait
# Connect prompts for the password through hidden input, never a command argument.
nixmox tui --installer
```

Reads: `/network/status`, `/network/wifi/networks`. All interfaces share status/events.

| Command type | Payload / result |
| --- | --- |
| network.check | `{}`; refreshed local network status |
| network.wifi.scan | `{}`; network list at `/network/wifi/networks` |
| network.wifi.connect | `network_id`, optional `credential_ref`; local connection result |
| discovery.search | `{}`; bounded search result with discovery IDs |

All four use [node-local admission](api-and-interfaces.md#operation-registry-and-scope).
`POST /api/v1/network/wifi/credentials` accepts `{network_id, password}` over the installer's HTTP console,
redacts it before middleware, and returns `{credential_ref, expires_at}`
with a five-minute staging lifetime. Connect binds the reference to that network;
open networks omit it. Credentials never enter the ordinary command payload.

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

Select a cluster → show cluster name/ID and gateway → Request join → Awaiting
cluster approval → pending membership → select an existing configuration or Create
configuration. Creation uses the shared generation forms, then returns to target preparation. Configuration
assignment alone never grants full membership. Installed nodes use deployment or
verified adoption; installer nodes proceed through preparation and installation.
Configured address/name submits the same request and waits for approval. Cluster
Hosts → Join requests exposes Approve/Reject with node/address/key details.

```text
USB boot → network setup → Clusters → Request join → cluster approval
Browser at IP:63170 ───────────┘                            ↓
Existing cluster → Discovery → installer → Add ────────→ pending
                                                           ↓
                                             configuration → prepare/install
```

Ethernet DHCP automatically enables the media's management interface, browser
gateway, and discovery; no local TUI confirmation is needed. Show current console
URLs on the TUI and in discovery records. A headless host can be configured at
`http://<installer-ip>:63170`, or through an existing cluster's Discovery → node
→ Add, without a node-side prompt. Awaiting approval has no cluster data/broker
access; approved pending leaves stream onboarding tasks/progress normally.
Browser and TUI reflect the same persisted enrollment/task state; joining
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
  "sops": {"updater": "anchors", "credential_ref": null, "update_rules": [], "state": "skipped", "reason": "no_rules_configured"},
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
nixmox deploy install cedar --preparation prep_01 --builder birch --wait
```

Preparation collects hardware, disk choices, and optional SOPS updates together.
Commit hands the reviewed diff to GitOps, pins the accepted commit, and replicates
it. No remote push is required. Installation checks target identity, config export,
Nixmox enablement, network handoff, and evaluated disk layout before effects.
Build on birch if desired; cedar builds the same pinned source using signed caches
or local compilation, without repository write credentials. Builds expose the same
[progress feedback](deployment-and-rollback.md#required-build-and-deployment-progress)
as ordinary deployments, including cache sources, rates, active builds, and bars.

Use the configuration's compatible [hardware/disk templates](configuration-generation.md#dedicated-hardware-template).
Auto-select a sole template; otherwise prompt or accept `--hardware-template` /
`--disk-template`. Values are typed fields containing observed device IDs.
Without a disk renderer, require one or an explicitly selected complete layout
whose evaluated devices match the selected disks.

Browser/TUI: Hosts → Set up → select/create configuration → Hardware / Disks /
optional Secrets → combined diff → Commit locally → Install review → task result.
Reuse generation forms when creating a configuration during setup.

## Confirmation and checkpoints

Use the [shared review rules](api-and-interfaces.md#reviews-and-concurrency) and
[task recovery contract](task-engine.md#admission-and-execution).

Installation review binds preparation version, node ID, observed device facts,
selected disks, and candidate/source. Require a typed stable path for **every**
erase target, checked against fresh target observations; a supplied path alone is
not authorization. Show configuration/commit, builder, disk model/serial/capacity,
SOPS status, and network handoff. Preparation and source must be ready before effects.

Install may start without approval and pause for review. Completed installation
continues to reboot/observation, not partitioning again. A partial destructive
retry needs fresh device observations as well as a new review.
Re-running preparation creates a new diff/commit; it does not reinstall.

## Installation backend and handoff

[Install](deployment-and-rollback.md#reboot-into-installer-and-fresh-install)
uses this flow for both first installation and replacement of an existing system,
after Nixmox has entered and authenticated the installer. Nixmox owns boot selection,
source/candidate pinning, confirmations, progress, and recovery. Verified build
outputs must be local before destructive execution. The installer builds the pinned
source through normal [Nix substitution/building](cache-gateway.md), reusing remote
builds where available. Keep required outputs and handoff state outside erase
targets. Cluster orchestration remains over NATS.

The Nixmox installer agent runs Disko/Nix installation tools; nixos-anywhere is
excluded. There is no separate output-transfer mechanism. Installer entry uses
`kexec-tools` or the profile's [reboot adapter](deployment-and-rollback.md#reboot-adapters).

Verify actual filesystems, firmware files, and boot entries before selecting the
installed system and rebooting. A successful shell exit alone is not readiness;
phase receipts and interruption handling follow the task recovery contract.

Preserve identity, scoped enrollment evidence, required host/decryption keys, and
network settings using the [state inventory](identity-and-trust.md#generated-and-preserved-state).
Copy Wi-Fi profiles only to a compatible target backend; otherwise require working
alternative connectivity before entry. Keep handoff outside erase targets and
remove consumed copies; never copy the whole old root or credential store.

For the `rpi-uboot-sd` layout, preparation separates **erase NVMe** from **preserve
SD partitions/update boot files**. Verify observed partition identifiers, mount the
installed `/boot` and `/boot/firmware` on SD, and leave the SD installer root intact.
The preserved partition selectors and boot-update authorization are distinct from
Disko's destructive device set. The installed configuration references stable IDs.
Do not rewrite the SD partition table or format a live installer filesystem.
Updates to shared boot files require a profile-tested staging/recovery protocol.
For `rpi-eeprom`, preserve the independently bootable SD installer and create the
installed system's firmware boot files on NVMe. Do not reuse the SD-boot/NVMe-root
disk template unchanged; the chosen adapter determines the required boot layout.

## Optional SOPS contract

Bundle an anchor-based YAML updater; a custom updater may override it. Configure
under `services.nixmox`:

```nix
{
  services.nixmox.prepare.sops = {
    credentialRef = "sops-editor";
    updateRules = [
      "^secrets/hosts/{{ .HostName }}/.*$"
      "secrets/secrets.yaml"
    ];
    updaterRef = null; # Bundled anchor updater; set a reference for a custom updater.
  };
}
```

Render `updateRules` with preparation values, then match each result **exactly**
against `creation_rules[].path_regex` in the repository's `.sops.yaml`. These select
existing rule expressions, not regexes run against other regexes. Update every
matching rule; an unmatched selector reports an error rather than creating a rule.
Escape substituted host names as regex literals. Literal selector text is preserved;
use an escaped dot and anchors when the SOPS rule itself requires an exact file match.

During prepare, obtain the age recipient from the host's SSH Ed25519 **public** key.
Create/reuse a stable node-ID-based anchor in the top-level `keys` sequence and add
its alias to the age list in **every key group** of each selected rule. Treat a
direct rule-level `age` field as one group. Use SOPS 3.11+ list-form age recipients;
normalize scalar age lists when necessary. Example for cedar:

```yaml
keys:
  - &admin age1ADMIN...
  - &nixmox_n_cedar age1CEDAR...
creation_rules:
  - path_regex: '^secrets/hosts/cedar/.*$'
    key_groups:
      - age: [*admin, *nixmox_n_cedar]
  - path_regex: 'secrets/secrets.yaml'
    key_groups:
      - age: [*admin, *nixmox_n_cedar]
```

Use a YAML syntax-tree editor: preserve unrelated rules, comments, order, recipients,
and group thresholds; avoid duplicate anchors/aliases on repeated prepare. Detach
shared group aliases before editing if they also affect unselected rules. Key
replacement that would change recipients outside the selected scope requires
explicit scope review; never silently rewrite other hosts' access.

Determine affected encrypted files using SOPS's effective rule selection. With
the configured age credential, run `sops updatekeys --yes <file>` for each affected
file; show recipient additions/removals and the combined diff in preparation review.
Commit `.sops.yaml` and encrypted-file updates together with hardware/disk changes.
Only the credential-holding executor handles the private updater key; host private
keys and plaintext secrets never enter Git, task payloads, or logs.

Empty `updateRules` or no credential: show Skipped and leave files unchanged.
A configured credential that cannot decrypt, or an updater failure, blocks commit
until resolved or the SOPS step is explicitly deselected. Never claim skipped work
establishes secret access.

A custom updater receives the worktree, node ID/name, public recipient, rendered
selectors, and allowed paths. It replaces the YAML-edit step and returns affected
encrypted paths and a structured result. Nixmox retains credential handling,
`updatekeys`, review, and commit ownership. Select it with `prepare.sops.updaterRef`.

## API

Reads: `/hosts/{id}/disks`, `/observations/{id}`, `/preparations/{id}` and its `/diff`.
Installation uses [deployment.install](deployment-and-rollback.md#reboot-into-installer-and-fresh-install).

| Command type | Required payload / optional fields |
| --- | --- |
| host.bootstrap | `ssh_target`, `credential_ref`, `host_key_fingerprint` |
| onboarding.inspect | `host_id`; returns `observation_id` |
| onboarding.prepare | `host_id`, `configuration_id`, `observation_id`, `template_values`; optional hardware/disk template IDs |
| onboarding.commit | `preparation_id`; expected preparation/repository versions; returns pinned source and artifact IDs |

Media payloads are defined in [installer images](installer-images.md#media-api).
Preparation detail/diff include the expected preparation/repository versions captured
with the rendered diff. `onboarding commit --expected-version` checks its preparation
entry and copies the same repository entry; conflicts require renewed preparation
and review. `onboarding show --diff` displays these versions with the combined diff.

`onboarding.prepare` accepts optional `hardware_template_id` and `disk_template_id`,
typed `template_values` grouped by `hardware` and `disk`, plus observation ID. Disk
fields contain selected disk IDs, not client-supplied device facts. Resolve
omitted IDs using the single-template rule and persist both IDs and source revision
in preparation. API callers cannot replace service-collected hardware or disk facts
with template values.

CLI erase review requires one `--confirm-device` per selected device. Initial
submission may pause without them; boot approval and `apply --yes` never authorize erasure.
