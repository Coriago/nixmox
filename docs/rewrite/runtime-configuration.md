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

This node starts unjoined, advertises discovery, and serves its console on the
selected management interface. It can create a cluster, register a public remote
repository, pull its base, and generate local changes without Git write/SOPS
credentials. Private repositories need read access. It remains pending after
cluster approval until deployment is verified. Local-only repositories are not
supported.

## Configured cluster and repositories

```nix
{ inputs, ... }: {
  imports = [ inputs.nixmox.nixosModules.default ];
  services.nixmox = {
    enable = true;
    cluster = {
      name = "homelab";
      joinAddress = "alder.example:63170";
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
| http.port | integer / 63170 | Browser, API, and cache gateway |
| nats.clientPort | integer / 63171 | Embedded broker client listener, loopback only |
| nats.routePort | integer / 63172 | Permanent broker routes on management interfaces |
| nats.leafPort | integer / 63173 | Pending-node leaf connections on management interfaces |
| network.managementInterfaces | list of strings / [] | Empty means loopback-only; selected interfaces permit admin access |
| network.preferredInterfaces | list of strings / [] | Agent endpoint preference; empty uses reachable allowed endpoints |
| network.allowFallback | bool / true | Permit less-preferred allowed endpoints |
| openFirewall | bool / true | Open required ports only on management interfaces |
| discovery.enable | bool / false | Advertise unjoined node or cluster on allowed LAN interfaces |
| discovery.port | integer / 63174 | UDP discovery; discovering peers must use the same port |
| cluster.name, cluster.joinAddress | nullable strings / null | Both set submits a join request awaiting cluster approval; one set alone is invalid |
| configuration | nullable {repository, name} / null | Intended configuration association |
| repositories | attribute set / {} | Named repository declarations; remoteUrl required for each |
| credentials | attribute set / {} | Named kind and runtime secretFile references |
| capabilities.build | bool / true | Accept local builds |
| capabilities.install, mediaWrite, power | bool / false | Permit respective effects on this node |
| deployment.keepGenerations | positive integer / 5 | Minimum recent outputs retained, in addition to active pins |
| cache.enable | bool / true | Local streaming gateway and Nix substituter integration |
| cache.serve | bool / true | Permit eligible member to serve its local store; independent of pulls |
| cache.signingCredentialRef | nullable string / null | Nix cache signing credential; null disables adding signatures, not serving already trusted outputs |
| cache.maxConcurrentTransfers | positive integer / 4 | Independently bound outgoing gateway downloads and incoming store exports |
| telemetry.otlp.enable | bool / false | Export OTel; local diagnostics still work |
| telemetry.otlp.endpoint | nullable string / null | Required when export is enabled |
| prepare.sops.updateRules | list of strings / [] | Templated exact selectors for `.sops.yaml` path_regex values; empty skips SOPS mutation |
| prepare.sops.updaterRef | nullable string / null | Null uses bundled anchor updater; otherwise selects a custom YAML updater |
| prepare.sops.credentialRef | nullable string / null | Age credential for updating encrypted-file recipients; null skips SOPS mutation |
| media.profiles | attribute set / {} | Additional Git-backed installer recipes; built-in x86/Pi profiles remain available; see [image contract](installer-images.md#custom-profiles) |

Repository entries require `remoteUrl`, default to `upstreamBranch = "main"`, and
have no credential by default. A module declaration registers repository settings;
import/fetch tasks populate each node's local checkout from the remote as needed.
JetStream replication settings belong to cluster storage, not individual Git
repositories. There is no full-Git replica count or placement option.
Installer media enables install and discovery, selects identity using the
[fresh-boot/handoff rules](identity-and-trust.md#reusable-installer-identity), starts
the browser gateway, and automatically launches `nixmox tui --installer` on the local
console. Its network profile attempts wired DHCP; connected Ethernet and explicitly
connected Wi-Fi become management interfaces automatically, enabling headless access.
This is the media profile; installed nodes retain the explicit interface policy above.
The daemon starts independently of the TUI and network setup. No shared identity
or repository-write token is embedded in the image. See
[installer startup](host-onboarding.md#installer-startup-and-network-setup).
Media/reboot and kexec variants use the same installer module and accept a protected
identity handoff instead of generating a replacement identity. `capabilities.install`
permits installation effects; actual availability also requires a verified
installer environment or supported kexec/reboot entry. Reboot entry includes a
compatible boot adapter plus existing installer media or a safely formattable spare
device; enable scoped media-write capability for its confirmed preparation. Otherwise expose
`INSTALLATION_UNSUPPORTED`, even when this option is enabled.

## Service ports

Use the following configurable defaults, including overrides for embedded NATS;
do not inherit the broker's conventional ports.

| Port | Transport | Listener |
| --- | --- | --- |
| 63170 | TCP | Browser, API, CLI, enrollment, and cache gateway |
| 63171 | TCP | NATS clients; loopback only |
| 63172 | TCP | Permanent NATS routes |
| 63173 | TCP | Pending-node NATS leaf connections |
| 63174 | UDP | Discovery |

`/cache/` is local-only; `/store-cache/` serves authorized cluster peers. Both share
`http.port`; no additional public cache port is opened. Derive the local Nix
substituter URL from this port and advertise actual peer endpoints. Store serving
is embedded in Nixmox using the Go NAR library; no companion service is required. See
[cache gateway](cache-gateway.md) for configuration and streaming semantics.

This block is in IANA's private-use range (49152–65535), which has no assigned
standard services. It is a Nixmox default, not an exclusive registered allocation;
other software can still choose it.
[IANA port ranges](https://www.iana.org/assignments/service-names-port-numbers/)

Linux's default automatic source-port range ends at 60999. The NixOS module must
also reserve configured listener ports in `net.ipv4.ip_local_reserved_ports`, merging
with other reservations rather than overwriting them, so a widened ephemeral range
does not allocate these ports automatically. Reservation does not prevent another
application from explicitly binding them.
[Linux port allocation](https://docs.kernel.org/networking/ip-sysctl.html#ip-variables)

Validate ports as integers 1–65535 and reject overlapping Nixmox TCP listeners.
Bind required listeners before reporting ready; on conflict, report the listener,
address, port, and configuration key. Do not kill the owning process or silently
choose another port. Broker monitoring/WebSocket listeners are disabled unless
separately designed and configured.

Machine TLS/authentication is mandatory and managed automatically, including the
loopback client listener; there is no anonymous NATS mode or user-supplied broker
password. Configure each client/route/leaf TLS block independently. See
[machine permissions](identity-and-trust.md#managed-tls-and-nats-permissions) and
[generated/preserved paths](identity-and-trust.md#generated-and-preserved-state).

Firewall rules use configured ports and management interfaces, never the loopback
client port. Advertise actual gateway/route/leaf endpoints; peers must not assume
the remote node uses their own port settings. A custom discovery port must match
across discovering peers; explicit address enrollment remains available.

Override example:

```nix
{
  services.nixmox = {
    http.port = 63270;
    nats = {
      clientPort = 63271;
      routePort = 63272;
      leafPort = 63273;
    };
    discovery.port = 63274;
  };
}
```

Acceptance: an occupied listener fails clearly without port hopping; custom ports
flow through CLI defaults, discovery, enrollment, and firewall rules; reservations
preserve other services' entries; an unrelated NATS instance can coexist.

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
  join_address: alder.example:63170
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
credential-capable full member, never echoed back.

## Equivalent UI operations

| CLI | Browser | TUI |
| --- | --- | --- |
| `nixmox settings show` | Settings → Runtime | Settings → Runtime → Enter |
| `nixmox repositories remote set lab --url https://github.com/example/nixos-config.git` | Repositories → lab → Settings → Remote | Same screen → Remote → Enter |
| `nixmox credentials import github-writer --kind git-http-token --node alder --file /run/keys/token` | Settings → Credentials → Add → storage node | Settings → Credentials → Add; hidden input |
| `nixmox repositories credential set lab github-writer` | Repositories → lab → Credentials | lab → Settings → Credentials |

Credential import reads the local file without logging its contents and sends it
over a protected connection to the chosen replica. Reject import over a plaintext
remote gateway; use local access or a protected endpoint/tunnel. Never put tokens
in command arguments. Application-secret replicas are provisioned independently;
machine certificate issuance follows the managed TLS contract.
