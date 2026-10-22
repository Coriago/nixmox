# Cluster Nix cache gateway

Every node offers one local Nix substituter that streams outputs from reachable
cluster members. Builders use it before compiling; deployment targets and media
writers use it to pull completed outputs. Any eligible member retaining an output
can serve it, whether it built or imported it. There is no designated cache leader
and no automatic replication of every output.

## Routes and transport

Local Nix uses HTTP; daemon-to-daemon transfers use automated TLS:

| Route | Listener | Access / responsibility |
| --- | --- | --- |
| `/cache/` | `http.port` / 63170 | Local machine only; aggregate peer lookup and stream a selected peer's response to local Nix |
| `/store-cache/` | `machine.port` / 63175 | Authorized cluster machines over mTLS; serve only valid outputs present in this node's Nix store |

Both namespaces implement `GET` and `HEAD` for `nix-cache-info`,
`{store-hash}.narinfo`, and `nar/{store-hash}/{nar-hash}.nar`. Gateway narinfo URLs
use that gateway-relative archive path, never an unconstrained upstream URL.
The archive identity binds the same uncompressed NAR hash/size across peer retries.
HEAD performs bounded lookup/metadata validation without exporting archive bytes.
`/cache/` remains local-only; `/store-cache/` exists only on the machine listener.
Neither namespace accepts uploads or arbitrary proxy destinations.

Enforce local access from the connection/listener, not the Host header or forwarded
headers. Apply machine trust to peer access. Pending installers may pull their
task-authorized closures; enrollment alone does not make them trusted producers.
Peer serving never fetches from another cache, invokes a build, or redirects back
to a gateway. This prevents recursive proxy loops.

NATS carries peer capabilities, endpoint updates, availability hints, and progress.
Peer artifact bytes travel directly over managed HTTPS, never through NATS/JetStream. No
HTTP-over-NATS or generic SSH-over-NATS fallback is provided. SSH remains available
for agent bootstrap. Deployment commands reach targets through NATS; targets pull
outputs locally, so remote activation does not require an SSH push.

Use advertised scheme/address/port and stable node ID, with the configured
LAN/virtual-interface preference policy. Never assume peers use the local port.
An unreachable peer is not a reason to change the approved output or interface policy.

```text
Nix on cedar → http://127.0.0.1:63170/cache/ → https://alder:63175/store-cache/ → alder's Nix store
                                           → https://birch:63175/store-cache/ → birch's Nix store
                  NATS: discovery, status and task progress only
```

## Lookup, streaming and failure behavior

1. Query eligible peers concurrently with bounded concurrency and deadlines for
   the requested store hash. Announcements are hints; verify availability now.
2. Return valid narinfo with a gateway-relative NAR URL. Preserve signatures and
   integrity metadata; safely rewrite only transport locations. Different paths
   in one dependency closure may come from different peers.
3. Stream from the selected peer with bounded buffers and backpressure. Do not
   stage whole archives in memory or on disk. The gateway stores no package bytes;
   the receiving Nix process imports outputs into its normal Nix store.
4. Before sending the body, an unreachable/missing/stalled peer can be replaced.
   After streaming starts, terminate a failed response and retry the exact archive
   in a new request. Never splice streams. Alternatives must match the issued URL's
   NAR and encoded archive identity, hash, size, and compression.
5. Bound retries, deprioritize failing peers, and probe for recovery. Distinguish a
   verified miss (404) from unavailable peers (503). Never report success for a
   partial transfer. Cancellation closes upstream requests, files, and exporters.

Normal builds may fall through to configured external substituters or compilation.
An approved deployment must obtain its exact candidate: exhausted transfer attempts
report `ARTIFACT_UNAVAILABLE`, not a silently rebuilt or substituted candidate.
Use bounded Nix connection/retry settings so gateway failure does not indefinitely
block external caches. No guarantee of availability exists when all holders are offline.

Tasks pin required outputs at their holders until completion/cancellation and
coordinate retention with generation policy. Serving holds a GC root while reading.
An availability hint never replaces a pin; removed/GC'd outputs must become misses.
Nix verifies hashes and signatures; never disable signature checking to make this
work. Supply a `nix-cache-signing-key` credential through a Nix runtime-file
reference or runtime credential import, like Git credentials, and select it with
`cache.signingCredentialRef`. Distribute its public key through authenticated cluster
configuration; private keys stay on their configured credential holders.
Never treat an unsigned candidate as trusted merely because it came from a member.

## Serving readiness and cross-host builds

`/store-cache/` is online only with `cache.serve`, eligible membership, and a valid,
locally available selected signing credential. No key is generated automatically.
Unset, missing, invalid, or unreadable credentials keep serving offline, even for
outputs with existing signatures. Authorized requests return 503; other machine
routes remain available. Validate the key and distribute its public trust before
advertising serving readiness. Credential loss withdraws that readiness.

With `cache.enable`, `/cache/` remains responsive regardless of local serving state.
It can pull from ready peers; unavailable peers produce bounded failures. Receiving
nodes need trusted public keys, not private signing credentials. Local builds and
same-host Apply/media writing remain available subject to their normal prerequisites.

| Operation | Cache prerequisite |
| --- | --- |
| Build on the consuming host | No local signing credential required |
| Build for another deployment target or media writer | Builder serving ready; consumer gateway ready and trusts the signer |
| Transfer an existing candidate | Exact output available from a ready trusted holder, or already verified locally |

UI builder choices/actions requiring cross-host output transfer stay disabled with
the missing prerequisite shown. Automatic selection excludes ineligible builders;
an explicit choice fails with `BUILDER_UNAVAILABLE` and the cache reason before
building. Recheck readiness at execution. If serving fails after admission, block
or reconcile the transfer while retaining the pinned candidate; never activate an
incomplete transfer or rebuild a different output silently.

## Configuration and observable behavior

```nix
{
  services.nixmox = {
    enable = true;
    http.port = 63170;
    credentials.cache-signer = {
      kind = "nix-cache-signing-key";
      secretFile = "/run/credentials/nixmox/cache-key";
    };
    cache = {
      enable = true;
      serve = true;
      signingCredentialRef = "cache-signer";
      maxConcurrentTransfers = 4;
    };
  };
}
```

Defaults: gateway enabled, serving permitted, four transfers, no signing credential.
Serving starts offline until configured; disabling serving does not disable pulls.
Derive the local substituter from `http.port` (`http://127.0.0.1:63170/cache/` here),
preserve external substituters, and install authorized peers' public signing keys
in Nix trust settings. Private keys use the credential store or runtime files,
never the Nix store. Standalone agents configure the same Nix integration.
No additional public port, archive directory, disk budget, or replica count is needed.
`maxConcurrentTransfers` independently bounds incoming exports and outgoing pulls;
they must not share a semaphore that can deadlock a local pull through the gateway.

```sh
nixmox credentials import cache-signer --kind nix-cache-signing-key --node alder --file /run/keys/cache-key
nixmox cache credential set cache-signer
nixmox cache status --json
nixmox cache locate /nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-example
nixmox apply cedar
```

Read APIs: `GET /api/v1/cache/status` and
`GET /api/v1/cache/locations?path=<url-encoded-store-path>`. The latter performs a
bounded live lookup; it is not a durable claim of possession. Example status:

```json
{
  "node_id": "n_cedar",
  "version": 12,
  "observed_at": "2026-10-21T14:00:00Z",
  "local_url": "http://127.0.0.1:63170/cache/",
  "gateway_state": "ready",
  "serving": false,
  "serving_reason": "SIGNING_CREDENTIAL_REQUIRED",
  "active_transfers": 1,
  "peers": [
    {"node_id": "n_alder", "state": "reachable"},
    {"node_id": "n_birch", "state": "unavailable"}
  ]
}
```

`gateway_state` is `ready`, `disabled`, or `unavailable`; readiness does not guarantee
that peers hold a requested output. `serving_reason` is null when serving; otherwise report `SERVING_DISABLED`,
`MEMBERSHIP_INELIGIBLE`, `SIGNING_CREDENTIAL_REQUIRED`, or
`SIGNING_CREDENTIAL_UNAVAILABLE` with details. Publish changes after configuration,
runtime credential selection, and credential loss/recovery; re-evaluate disabled actions.

Browser/TUI: Settings → Cache shows this node's gateway and peer health; Locate
accepts a store path and lists verified holders or lookup failures. Apply, Install,
and Media tasks automatically use the gateway. Their progress shows actual source
node/cache, bytes, speed, attempts, and failures, not just the localhost URL.
Publish versioned cache status through the common event API; reconnect refreshes
the snapshot. OTel covers lookups, streaming, peer failures, bytes, and cancellation.

## Embedded store serving

Nixmox serves the local store in-process using a pinned `go-nix/pkg/nar` library
for NAR serialization. No Harmonia service or per-download Nix subprocess is
required. Use the same implementation in installed nodes and installer agents.

Start with uncompressed NARs (`Compression: none`), with FileHash/FileSize equal to
NarHash/NarSize. Use bounded streaming buffers and backpressure. Interrupted
downloads restart; Range requests may receive the complete 200 response.

Obtain metadata from Nix through a dedicated adapter. Maintain an indexed,
refreshable store-hash lookup; update after builds/imports/GC and reconcile external
store changes. Do not scan the entire store per request or freeze a startup catalogue.
Acquire a GC root and recheck validity before exporting; release the serving root
on completion or cancellation while preserving task/generation pins. Preserve
existing signatures and add the configured Nixmox signature using Nix's fingerprint.
Serving errors are scoped to the request and reported through cache health/telemetry.

## Verification

Real Nix imports must verify split-peer closures, bounded streaming, trusted
signatures, mid-stream failure, cancellation/GC safety, and restricted pending access
and accurate peer progress. Test absent/invalid keys, config/runtime credential
activation, and key loss: serving and cross-host build eligibility change while
gateway pulls/local builds remain available. Receivers require no signing key.
