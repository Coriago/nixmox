# Runtime and NixOS configuration

These option names and defaults are the proposed rewrite module contract. Paths
containing secrets are runtime string paths, never Nix path literals that copy
secret contents into the store. UI-created settings are persisted by Nixmox.

## Minimal installed node

```nix
{ inputs, ... }: {
  imports = [ inputs.nixmox.nixosModules.default ];
  services.nixmox = {
    enable = true;
    network.managementInterfaces = [ "enp1s0" ];
    discovery.enable = true;
  };
}
```

The node starts unjoined and exposes discovery and its console on the selected
management interface. Joining requires cluster approval; full membership requires
verified deployment or adoption.

## Configured cluster and repositories

```nix
{ inputs, ... }: {
  imports = [ inputs.nixmox.nixosModules.default ];
  services.nixmox = {
    enable = true;
    cluster = {
      name = "homelab";
      joinAddress = "alder.example:63175";
    };
    network = {
      managementInterfaces = [ "enp1s0" "tailscale0" ];
      preferredInterfaces = [ "tailscale0" "enp1s0" ];
      allowFallback = true;
    };
    discovery.enable = true;
    repositories = {
      lab = {
        remoteUrl = "https://github.com/example/nixos-config.git";
        upstreamBranch = "main";
        credentialRef = "github-writer";
      };
      services = {
        remoteUrl = "https://github.com/example/services.git";
        upstreamBranch = "master";
      };
    };
    configuration = { repository = "lab"; name = "alder"; };
    credentials.github-writer = {
      kind = "git-http-token";
      secretFile = "/run/credentials/nixmox/github-token";
    };
    credentials.cache-signer = {
      kind = "nix-cache-signing-key";
      secretFile = "/run/credentials/nixmox/cache-key";
    };
    credentials.sops-editor = {
      kind = "sops-age-key";
      secretFile = "/run/credentials/nixmox/sops-age-key";
    };
    cache.signingCredentialRef = "cache-signer";
    prepare.sops = {
      credentialRef = "sops-editor";
      updateRules = [ "^secrets/hosts/{{ .HostName }}/.*$" "secrets/secrets.yaml" ];
    };
    capabilities = {
      build = true;
      install = true;
      mediaWrite = true;
      power = true;
    };
    deployment.keepGenerations = 5;
    cache = { enable = true; serve = true; maxConcurrentTransfers = 4; };
    telemetry.otlp = {
      enable = true;
      endpoint = "http://collector.internal:4318";
    };
  };
}
```

Assigning `configuration` advertises intent; the service must verify a deployment
receipt or adopted running system before full membership. `credentialRef` does not
require every node to possess the credential. Only configured replicas execute
credential-dependent operations. Git remote configuration never triggers a push.

## Option reference

| Option below services.nixmox | Type/default | Behavior |
| --- | --- | --- |
| enable | bool / false | Install CLI and daemon service |
| dataDir | string / /var/lib/nixmox | Persistent identity, tasks, repositories, receipts |
| http.port | integer / 63170 | Plain HTTP console API, local cache gateway, and peer store cache |
| machine.port | integer / 63175 | Plain HTTP bootstrap enrollment and signed membership-status records |
| nats.clientPort | integer / 63171 | Embedded broker client listener, loopback only |
| nats.routePort | integer / 63172 | Permanent broker routes on management interfaces |
| nats.leafPort | integer / 63173 | Pending-node leaf connections on management interfaces |
| network.managementInterfaces | list of strings / [] | Empty means loopback-only; selected interfaces permit admin access |
| network.preferredInterfaces | list of strings / [] | Agent endpoint preference; empty uses reachable allowed endpoints |
| network.allowFallback | bool / true | Permit less-preferred allowed endpoints |
| openFirewall | bool / true | Open required ports only on management interfaces |
| discovery.enable | bool / false | Advertise unjoined node or cluster on allowed LAN interfaces |
| discovery.port | integer / 63174 | UDP discovery; discovering peers must use the same port |
| cluster.name, cluster.joinAddress | nullable strings / null | Both set submits a join request to the peer's machine address (`host:port`) awaiting cluster approval; one set alone is invalid |
| configuration | nullable {repository, name} / null | Intended configuration association |
| repositories | attribute set / {} | Named repository declarations; remoteUrl required for each |
| credentials | attribute set / {} | Named kind and runtime secretFile references |
| capabilities.build | bool / true | Accept local builds |
| capabilities.install, mediaWrite, power | bool / false | Permit respective effects on this node |
| deployment.keepGenerations | positive integer / 5 | Minimum recent outputs retained, in addition to active pins |
| cache.enable | bool / true | Local streaming gateway and Nix substituter integration |
| cache.serve | bool / true | Permit serving when membership and local signing credential are ready; independent of pulls |
| cache.signingCredentialRef | nullable string / null | Selected `nix-cache-signing-key`; unset/unavailable keeps store serving offline while the enabled gateway remains responsive |
| cache.maxConcurrentTransfers | positive integer / 4 | Independently bound outgoing gateway downloads and incoming store exports |
| telemetry.host.fastIntervalMs | integer / 500 | Fast host sampling and NATS publish interval; range 250–5000 ms |
| telemetry.host.filesystemIntervalMs | integer / 10000 | Independent filesystem sampling; range 1000–60000 ms, at least fastIntervalMs |
| telemetry.otlp.enable | bool / false | Export application and host metrics via OTel; live host telemetry and local diagnostics work independently |
| telemetry.otlp.endpoint | nullable string / null | Required when export is enabled |
| telemetry.otlp.metricsIntervalMs | integer / 5000 | Metrics export interval; range 250–60000 ms, at least fastIntervalMs |
| prepare.sops.updateRules | list of strings / [] | Templated exact selectors for `.sops.yaml` path_regex values; empty skips SOPS mutation |
| prepare.sops.updaterRef | nullable string / null | Null uses bundled anchor updater; otherwise selects a custom YAML updater |
| prepare.sops.credentialRef | nullable string / null | Age credential for updating encrypted-file recipients; null skips SOPS mutation |
| media.profiles | attribute set / {} | Additional Git-backed installer recipes; built-in x86/Pi profiles remain available; see [image contract](installer-images.md#custom-profiles) |

Repository entries require `remoteUrl`, default to `upstreamBranch = "main"`, and
have no credential by default. A module declaration registers repository settings;
import/fetch tasks populate each node's local checkout from the remote as needed.

Installer media automatically enables connected Ethernet/explicitly connected Wi-Fi
as management interfaces; installed nodes retain the explicit policy above.
See [installer startup](host-onboarding.md#installer-startup-and-network-setup) and
[identity handoff](identity-and-trust.md#reusable-installer-identity).
`capabilities.install` permits effects but still requires a verified installer
or [supported entry route](deployment-and-rollback.md#reboot-into-installer-and-fresh-install).
Preparing spare boot media also requires scoped media-write capability.

## Service ports

Use the following configurable defaults, including overrides for embedded NATS;
do not inherit the broker's conventional ports.

| Port | Transport | Listener |
| --- | --- | --- |
| 63170 | TCP / HTTP | Browser, CLI/TUI API, local cache gateway, and peer store cache |
| 63171 | TCP / TLS | NATS clients; loopback only |
| 63172 | TCP / TLS | Permanent NATS routes |
| 63173 | TCP / TLS | Pending-node NATS leaf connections |
| 63174 | UDP | Discovery |
| 63175 | TCP / HTTP | Enrollment and public signed membership-status records |

Both cache namespaces use plain HTTP on `http.port`: `/cache/` is local-only,
and `/store-cache/` exposes signed local outputs to machines on the management
network without per-reader authentication. Derive the local Nix substituter URL
from `http.port` and advertise actual cache endpoints. Store serving is embedded
in Nixmox using the Go NAR library; no companion service is required. See
[cache gateway](cache-gateway.md) for configuration and streaming semantics.

CLI, TUI, and browser use HTTP for all console operations, including credential
and Wi-Fi submission. No console HTTPS listener, certificate setup, or TLS redirect
is required or provided in v1. Console requests and submitted secrets are unencrypted
on that connection; access is limited to the configured management network.

Nixmox serves no HTTPS. TLS protects NATS clients/routes/leaves only.
The machine HTTP listener serves enrollment and signed removal records, separate
from console/cache routes. Secrets travel between daemons through Core NATS.
External services retain their own transports. Advertise actual services and ports.

Reserve configured listener ports in `net.ipv4.ip_local_reserved_ports`, merging
with other services' reservations. This does not prevent explicit bind conflicts.

Validate ports as integers 1–65535 and reject overlapping Nixmox TCP listeners.
Bind required listeners before reporting ready; on conflict, report the listener,
address, port, and configuration key. Do not kill the owning process or silently
choose another port. Broker monitoring/WebSocket listeners are disabled unless
separately designed and configured.

NATS TLS/authentication is mandatory and managed automatically, including the
loopback client listener; there is no anonymous NATS mode or user-supplied broker
password. Configure each client/route/leaf TLS block independently. See
[machine permissions](identity-and-trust.md#managed-tls-and-nats-permissions) and
[generated/preserved paths](identity-and-trust.md#generated-and-preserved-state).

Firewall rules use configured ports and management interfaces, never the loopback
client port. Advertise actual console/machine/route/leaf endpoints; peers must not assume
the remote node uses their own port settings. A custom discovery port must match
across discovering peers; explicit address enrollment remains available.

## Ownership and reconciliation

Local process settings: compiled defaults → runtime YAML → explicitly provided CLI
flags. The NixOS module generates YAML; the service passes its path to
`nixmox serve --config /etc/nixmox/config.yaml`. Unknown fields fail startup.
The CLI endpoint is selected by `--endpoint`, then `NIXMOX_ENDPOINT`, then the local
configuration's gateway port on loopback; without local configuration it defaults
to `http://127.0.0.1:63170`.
With that local connection, `nixmox apply` uses the daemon's node identity and
assigned configuration. A remote endpoint selects the console connection only;
use an explicit target such as `nixmox --endpoint http://alder:63170 apply cedar`.

Equivalent standalone daemon configuration:

```yaml
schema_version: 1
data_dir: /var/lib/nixmox
http:
  port: 63170
machine:
  port: 63175
nats:
  client_port: 63171
  route_port: 63172
  leaf_port: 63173
discovery:
  enable: true
  port: 63174
network:
  management_interfaces: [enp1s0, tailscale0]
  preferred_interfaces: [tailscale0, enp1s0]
  allow_fallback: true
cluster:
  name: homelab
  join_address: alder.example:63175
repositories:
  lab:
    remote_url: https://github.com/example/nixos-config.git
    upstream_branch: main
configuration:
  repository: lab
  name: alder
```

The module serializes its camelCase options to these snake_case keys. The same
typed configuration parser serves module output and standalone YAML; defaults
must not be independently implemented in two places.

Repository declarations are cluster records keyed by alias. Equal declarations
converge on one ID. Conflicting declarations from different nodes report
`CONFIGURATION_CONFLICT`; last-writer-wins is forbidden. Declared fields are
read-only in the UI; missing fields may be set there. Removing a declaration
releases ownership, not the data. Repository deletion is explicit.

Credential references are cluster metadata, but secretFile paths are local to the
configured replica. A missing file marks that capability unavailable rather than
preventing the entire console from starting. UI writes cannot override a declared
secret source. Secrets entered through the UI are stored privately on a selected
credential-capable full member, never echoed back; remote delivery uses Core NATS.

## Settings UI

`nixmox settings show` and browser/TUI Settings → Runtime show effective values and
declaration ownership. Repository settings are under Repositories → Settings;
credential operations follow [identity and trust](identity-and-trust.md#credential-record-and-operations).
