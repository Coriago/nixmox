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

```nix
{
  imports = [ inputs.nixmox.nixosModules.default ];
  services.nixmox = {
    enable = true;
    package = inputs.nixmox.packages.x86_64-linux.default;
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
- **Host detail:** CPU utilization/model/cores, memory, load, interfaces/status/IPs/traffic, block devices/partitions/sizes/mounts, apply history and local job logs.
- **Live updates:** one reconnecting SSE connection per page sends only changed HTML fragments. Local telemetry is sampled at most once every two seconds; remote telemetry follows the ten-second heartbeat. No frontend build toolchain is required.
- **GitOps:** update availability and an on-demand preview of commits, changed files and diff statistics, comparing the last recorded applied revision to the available revision. This is a Git diff, **not** a NixOS service/package activation plan. Pull the revision you intend to apply; builds are pinned to the pulled commit, not a mutable checkout.
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

Write the resulting image to your chosen removable medium using your usual imaging tool. **Double-check the destination device.** The aarch64 image requires an aarch64 builder (or configured emulation) and a compatible board/bootloader; it is not a universal image for every SBC.

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

## Development and verification

```sh
nix develop --command go test -race ./...
nix develop --command go vet ./...
nix build .#checks.x86_64-linux.single-node-portal
nix build .#checks.x86_64-linux.jobs-runner
nix build .#checks.x86_64-linux.gitops-pull
nix build .#checks.x86_64-linux.gitops-apply
nix build .#checks.x86_64-linux.cluster-lifecycle
```

Use `path:.#...` instead of `.#...` when testing newly added, untracked files (Git flakes omit them). See [architecture](docs/architecture.md) for component boundaries and state compatibility.
