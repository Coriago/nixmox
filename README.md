# nixmox

A lightweight NixOS host and cluster console: server-rendered HTML, live updates over SSE, GitOps builds, and an embedded NATS control plane.

> **Trusted management networks only.** The HTTP API and NATS routes do not authenticate peers. Do not expose them to the Internet or an untrusted LAN. Browser same-origin checks are not authentication. Use an isolated management network/VPN and restrict firewall access. Power controls and disk installation are disabled by default.

## Run

```sh
nix develop
go test -race ./...
go run ./cmd/nixmox serve
# http://127.0.0.1:8080
```

Or `nix build` and `./result/bin/nixmox serve`. `nixmox status` queries the local agent; `nixmox join HOST:8080` joins an existing cluster. Create a cluster from the setup page first.

### NixOS module

The flake module defaults to this Nixmox release's package for the host architecture,
so enabling the service needs no package assignment or overlay. Set
`services.nixmox.package` only to override it with a custom build.

New hosts show a **SOPS age recipient** on the setup page and under host details.
Copy it into the host's `.sops.yaml` rules, run `sops updatekeys` for its encrypted
files, then commit and pull the changes before applying. With the sops-nix module
imported, use `sops.age.sshKeyPaths = [ "/etc/ssh/ssh_host_ed25519_key" ];`.
Nixmox derives the public recipient from that existing private SSH key; it does
not create keys or configure SOPS for you. Missing/unreadable keys show an explicit
status and are checked again within 30 seconds. A live installer's SSH identity
is temporary unless you preserve it during installation; check the installed
host's recipient before encrypting its secrets.

```nix
{
  imports = [ inputs.nixmox.nixosModules.default ];
  services.nixmox = {
    enable = true;
    advertiseAddr = "192.168.10.20"; # use an explicit address on multihomed hosts
    discovery = true;
    enablePower = true;
    allowConfigWrite = true;
    # enableProvision = true; # permits destructive installation over SSH
    gitops.repoUrl = "https://example.org/your/nixos-config.git";
    autoApply = false;
  };
}
```

The module opens TCP 8080/6222 and, when discovery is enabled, UDP 8099. Set `openFirewall = false` and manage access yourself if these ports must be interface-restricted. Standalone CLI defaults to loopback HTTP; the module listens on all interfaces. An unset advertise address selects the first up, non-loopback IPv4 interface.

Configuration precedence: defaults → YAML (`--config` / `NIXMOX_CONFIG`) → environment. New settings are `enable_power`, `enable_provision`, `discovery`, `join_seed`, and `inputs_file`, with corresponding `NIXMOX_` uppercase environment variables. Existing settings remain supported.

## Console workflows

- **Hosts:** online/offline inventory, apply/rollback, Wake-on-LAN start, shutdown. Host actions target the selected node, not the portal's local node.
- **Host detail:** revision sync and candidate preview, live host tasks and remote logs, apply history, and collapsible hardware/telemetry. Global Tasks aggregates reachable hosts and supports a live host filter.
- **Live updates:** one reconnecting SSE connection per page carries changed HTML fragments and incremental log chunks. CPU/memory/I/O/bandwidth update every two seconds; inventory and local capacity refresh every 30 seconds. Uptime ticks once per second. Connection loss is visible, with automatic reconnect and links to other hosts. No frontend build toolchain is required.
- **GitOps:** host inventory distinguishes repository updates, unapplied revisions, failures and boot staging. Pull, then queue a **Preview packages & configuration** on the host: it builds the pinned candidate without activation and compares packages and generated configuration against the running system. Source-level Git preview remains available. Preview cannot predict all activation-script effects; repeat it after changes to the revision or local inputs.
- **Operations:** LAN discovery, install/wipe, serial/canary/batched rollouts, host inputs, and a bounded daemon-log tail. Full durable daemon logs remain in `journalctl -u nixmox`.

### Power

Enable `enablePower`. Shutdown and reboot are scheduled **one minute later** and require exact-hostname confirmation in the API; the UI supplies this after a confirmation dialog. A successful job means the power action was scheduled, not that shutdown has completed.

Starting a powered-off machine requires another running node on its broadcast domain and firmware/NIC Wake-on-LAN support. The agent sends a magic packet using the target's last-known interface MAC. Inventory is saved periodically and on clean shutdown so offline targets remain visible after restart. WoL cannot start arbitrary hardware or cross routed networks without a relay.

### Joining and removing hosts

Discovery is opt-in IPv4 multicast (`239.255.80.99:8099`). Advertisements expire after 35 seconds and are **untrusted hints**. Verify the address before joining. Discovery never automatically admits a node.

Use **Leave cluster** on the target's detail page to remove a reachable host. The agent removes its Raft membership, clears its identity, archives old cluster storage, and asks remaining reachable peers to forget its route. The last survivor retains the cluster name in standalone mode. Local jobs, inputs, and apply history are retained. An explicit leave suppresses configured first-boot autojoining until the user joins again.

All participants should be reachable for removal. Partial peer-cleanup failures are returned in the response's `warnings` array; do not treat this as forced eviction of an offline node. Keep local state backups: cluster KV is a shared view, not a replacement for each node's local history.

### Rollouts

`POST /api/v1/rollout` accepts:

```json
{"strategy":"batch","batch_size":2}
```

- `serial`: one host at a time.
- `canary`: first host, a 30-second observation pause, then serial execution.
- `batch`: 1–8 concurrent hosts; wait for the whole batch before advancing.

Offline hosts block admission; failed jobs stop subsequent batches. Each node applies its **own pulled revision**. Pull and verify all nodes first. Disable independent automatic apply during a rollout. Use **one coordinator**: rollout admission/status is process-local, not a distributed lock or durable resumable workflow. Canary success is job completion, not an application-specific health probe. A daemon restart interrupts orchestration; inspect task state before retrying.

## Bootable installation media

```sh
nix build .#installer-iso                          # x86_64 USB/DVD hybrid ISO
nix build .#packages.aarch64-linux.installer-sd    # generic aarch64 SD image
```

Enable `services.nixmox.enableMedia = true` and open **Operations → Add a host · USB installer** to prepare and write the x86_64 ISO from the UI. Optionally include an SSH public key, select the attached drive, type its exact path, and follow write/readback verification progress. `services.nixmox.installerFlake` selects a trusted source/output (default `github:Coriago/nixmox#installer-iso`; pin a revision for reproducibility). The drive must be attached to the node serving the portal. Mounted/active disks and stale drive identities are rejected. The UI writes the whole image; it does not require pre-formatting.

On boot, the installer opens a network selector on the first local console when
no wired interface has a usable address and a Wi-Fi adapter is available. Select
the network, enter its password, and quit the selector when connected. Wired
boots skip the selector; nixmox and LAN discovery start independently. Network
setup can be reopened with `nixmox-wifi`. Connections are saved by NetworkManager
in the live environment; transferring them to an installed host will be part of
the host-install workflow.

See [design decisions and the complete bootstrap workflow](docs/ui-and-bootstrap.md) for configuration, validation, preview limits, and reasoning. You can still write CLI-built images using another imaging tool. **Double-check the destination device.** The aarch64 image requires an aarch64 builder (or configured emulation) and a compatible board/bootloader; it is not a universal image for every SBC.

The live image boots with a unique hostname, DHCP, discovery, and the portal enabled. SSH password authentication is disabled. Build an image containing your SSH public key for unattended remote installation:

```nix
# In your flake outputs:
packages.x86_64-linux.rescue = (inputs.nixmox.lib.mkInstaller {
  system = "x86_64-linux";
  format = "iso";
  sshKeys = [ "ssh-ed25519 AAAA... your-key" ];
}).config.system.build.isoImage;
```

1. Boot the new machine from the image; inspect its disks/IPs from the console.
2. Prepare a target flake with **disko**, correct disk identifiers, hardware configuration, an SSH key, and the nixmox module enabled.
3. On an existing node with `enableProvision = true` and SSH access, open Operations → Install / wipe. Enter `root@IP`, `flake#host`, and the exact target again to confirm erasure.
4. nixos-anywhere builds, partitions/formats using disko, installs and reboots. Follow its task log. It never selects a disk implicitly; the target flake defines what is erased.
5. Join from the installed node's portal, or set `services.nixmox.joinSeed = "SEED:8080"` in its target flake to join on first startup. If the seed is unavailable, the daemon remains accessible for a manual retry.

The same operation reprovisions an existing host. Remove it from its cluster first. The controller must already have suitable noninteractive SSH access; no private keys or passwords are embedded in the distributed images. The default live image has no authorized SSH keys.

See [nixos-anywhere's guide](https://github.com/nix-community/nixos-anywhere/blob/main/docs/quickstart.md) for hardware/disko preparation.

## Per-host inputs outside Git

Operations → Local host inputs edits a private, atomically replaced JSON file, by default `/var/lib/nixmox/inputs.json`. Supported values are numbers, booleans and strings; the example fan slider writes `max_fan_speed` as a percentage.

The NixOS module exposes these values as `config.nixmox-inputs`:

```nix
{ config, ... }: {
  # Replace with an option your own hardware module actually defines:
  hardware.settings.maxfanspeed = config.nixmox-inputs.max_fan_speed or 75.0;
}
```

Apply jobs pass `--impure` so Nix can read the local JSON. Pure/remote evaluation cannot see host-local inputs: provide defaults or explicitly distribute inputs in that workflow. Saving inputs does not automatically rebuild or immediately change hardware. **Do not put secrets in inputs**; evaluated values can enter the Nix store.

## Live GitHub end-to-end test

The separate [`e2e/`](e2e/README.md) flake provides real VM deployment
configurations with a marker, an extra package, and an activation service.
Run both parts as the **main final acceptance test**. For unpublished changes,
run `nix run path:.#github-e2e -- --source-tree "$PWD"` to test an explicit Git
snapshot of the working tree. After an authorized publication to GitHub `main`,
`nix run .#github-e2e` tests that published revision. It boots three fresh VMs, validates discovery
and both join paths, applies the pulled configuration to one node, then rolls it
out across the cluster. Part 2 tests quorum loss, recovery, graceful removal,
Raft de-escalation, and creating fresh clusters after removal.
Use `nix run .#github-e2e -- --part 1` to stop at a healthy, deployed cluster for
interactive testing. Resume teardown with `--part 2 --work-dir <printed-directory>`.
The [test flow](e2e/README.md) documents each assertion and the retained artifacts.

Set `services.nixmox.gitops.flakeDirectory` for a flake in a repository
subdirectory and `gitops.flakeAttribute` when the configuration attribute differs
from the hostname. YAML/environment equivalents are `flake_directory` /
`NIXMOX_FLAKE_DIRECTORY` and `flake_attribute` / `NIXMOX_FLAKE_ATTRIBUTE`.

## Development and verification

```sh
nix develop --command go test -race ./...
nix develop --command go vet ./...
nix build .#checks.x86_64-linux.single-node-portal
nix build .#checks.x86_64-linux.jobs-runner
nix build .#checks.x86_64-linux.usb-installer
nix build .#checks.x86_64-linux.gitops-pull
nix build .#checks.x86_64-linux.gitops-apply
nix build .#checks.x86_64-linux.cluster-lifecycle
```

Use `path:.#...` instead of `.#...` when testing newly added, untracked files (Git flakes omit them). See [architecture](docs/architecture.md) for component boundaries and state compatibility.

### Browser host onboarding

Open **Set up host** in the sidebar, or choose **Set up host** next to a discovered
live installer. The browser selects a configured repository writer, collects the
installer's hardware, public age recipient and whole-drive inventory, and renders
the repository owner's form. Review the generated file and type the exact drive
path before starting. The selected drive's model, capacity and serial are shown;
mounted disks, partitions and installer media are excluded.

Configure at least one installed cluster node as the repository writer:

```nix
services.nixmox = {
  gitops = {
    repoUrl = "git@github.com:owner/nix-config.git";
    branch = "main";
    sshKeyPath = "/run/keys/git-writer";
  };
  addHost = {
    form = {
      Name = { type = "hostname"; default = "newhost"; };
      Username = { type = "username"; default = "helios"; };
      StateVersion = { type = "text"; default = "26.05"; };
      Modules = {
        type = "multiselect";
        values = [ "workstation" "development" ];
        default = [ "workstation" ];
      };
      Drive = { type = "disk"; required = true; };
    };
    path = "modules/hosts/{{ .Name }}/configuration.nix";
    template = ./config.gotmpl;
    ageKeyFile = "/run/keys/sops-age";
    sops = {
      rulesFile = ".sops.yaml";
      files = [ "secrets/secrets.yaml" ];
      rulePathRegex = "secrets/secrets.yaml";
    };
  };
};
```

Git uses the writer's existing GitOps SSH or HTTPS credentials. `ageKeyFile` is a
runtime identity already authorized to decrypt the selected SOPS files; it must
not be a Nix store path. The installer receives neither credential. It needs its
own repository read access (or a publicly readable repository). With no SOPS
files, omit `sops` and `ageKeyFile`. Branch protection must permit the configured
writer to push; the workflow never force-pushes or bypasses protection.

The writer clones an isolated checkout, creates one configuration without
replacing an existing file, updates the selected SOPS rule and runs
`sops updatekeys --yes`, then commits and pushes the configuration and encrypted
files together. An earlier matching SOPS rule, ambiguous rule, unsupported
threshold/key-group rule, failed key update or failed push stops the workflow.
Only public recipients and encrypted secrets enter Git. Git/SOPS errors omit
command output that could disclose credentials.

The temporary installer's own GitOps task pulls and pins the published commit.
It verifies that the evaluated configuration enables nixmox, has the assigned
hostname and cluster seed, and that disko targets only the confirmed drive. Its
nixmox runs nixos-anywhere against itself using an ephemeral SSH daemon listening
only on loopback. No cluster SSH key is embedded in the image. The host SSH key
is copied locally into the installed system. NetworkManager Wi-Fi profiles are
copied locally with restricted permissions when compatible; otherwise installation
requires an explicitly confirmed alternative connection plan.

The UI shows task phases and logs through reboot and installed-host reachability.
Temporary installer sessions never join voting Raft. After the installed hostname
joins with the preserved public identity, setup is marked complete. Failures
retain logs and the published revision; retries require a fresh drive confirmation
and reuse the recorded revision. A completed installation is checked for join or
reboot without repeating disk erasure. Busy or changed disks stop a retry.

`services.nixmox.installerMode` enables destructive local onboarding endpoints and
is enabled by the live installer profile, not on normal installed hosts. Management
APIs use the existing trusted-management-network model; discovery is an untrusted
hint, so verify the selected node's address and drive before confirming.

The template contract remains in `onboarding-improvements.md`: strings are quoted
and escaped Nix literals, including interpolation; `.Hardware` is the complete
unmodified output of `nixos-generate-config --show-hardware-config --no-filesystems`.
Destination templates use validated plain values. The selected modules must supply
disko and nixmox. Browser onboarding requires `Name` (hostname) and `Drive` (disk)
fields; other fields are repository-defined.

A standalone preview/write CLI is also available with `add_host` YAML settings:

```sh
nixmox --config generator.yaml add-host --input observations.json
nixmox --config generator.yaml add-host --input observations.json --repo ./checkout --write
```

Input is JSON with `values`, `hardware`, `cluster_address`, and `disks`. The CLI
only generates a file; the browser workflow performs collection and orchestration.

Focused validation (no VMs):

```sh
nix develop --command go test ./internal/addhost ./internal/server ./internal/gitops ./internal/jobs
nix develop --command env NIXMOX_BROWSER_TEST=1 go test ./internal/server -run TestOnboardingBrowser
```

The browser test simulates installer API responses. Git/SOPS integration tests use
real encryption and disposable local repositories. Neither proves disk installation,
Wi-Fi reconnection, or reboot/join on actual hardware; the full VM acceptance remains
required before treating the destructive workflow as acceptance-tested.
