# Cluster Nix cache gateway

Build runs on the selected host. Its signed outputs become available in that
host's HTTP cache. When another host builds the same pinned source, Nix uses cached
outputs where available and compiles missing ones locally.

There is no separate output-transfer task or protocol. Git source bundles still
use JetStream; Nix outputs do not.

## Routes and transport

Both routes use plain HTTP on `http.port` (63170):

| Route | Access | Purpose |
| --- | --- | --- |
| `/cache/` | Local machine only | Aggregate peer caches for local Nix |
| `/store-cache/` | Configured management network | Read-only cache of this node's valid store outputs |

Support `GET` and `HEAD` for `nix-cache-info`, `{store-hash}.narinfo`, and
`nar/{store-hash}/{nar-hash}.nar`. HEAD returns metadata without exporting bytes.
Neither route accepts uploads or arbitrary proxy destinations.

Check local access from the connection, not HTTP headers. Peer cache readers do
not need certificates or task grants. Nix verifies signatures and hashes; HTTP
provides no confidentiality, so store outputs must not contain secrets.

NATS carries cache endpoints, public signing keys, availability hints, and progress.
Cache bytes use HTTP. A serving node never builds or fetches from another cache
in response to a download request.

```text
Nix on cedar → http://127.0.0.1:63170/cache/
              → http://birch:63170/store-cache/
              → http://alder:63170/store-cache/
              → external caches or local compilation on a miss
```

## Lookup and streaming

- Query advertised peers with bounded concurrency and deadlines. Use their actual
  ports and the configured interface policy; availability hints are not proof.
- Preserve narinfo signatures/hashes and rewrite only its archive URL to the local
  gateway path. Different paths in a closure may come from different peers.
- Stream with bounded buffers and backpressure. The gateway stores no archives;
  Nix imports into its normal store and verifies integrity/trust.
- Retry another peer before sending a body. After streaming begins, terminate a
  failed response; never splice streams. A retry must match the same archive
  identity, hashes, size, and compression.
- Distinguish a verified miss (404) from unavailable peers (503). Bound retries,
  close requests/exporters on cancellation, and never report partial success.

Nix must fall back to other substituters or local compilation when a cache fails.
Reject invalid hashes/signatures; never disable verification. A missing builder
must not block a capable target from building the same pinned commit, locked inputs,
attribute, and system. Missing source/dependencies or unsupported build systems
still fail clearly; do not silently select newer source or enable emulation.

Pin task/generation outputs locally. Each export holds a GC root and rechecks store
validity; release that root after the request. Cache hints are not retention pins.
Before activation or writing media, the consumer must have a verified local output.
Rebuilding the same source can produce different bytes: changed output evidence
requires a new review, not reuse of an old approval.

## Serving readiness and cross-host builds

Serving requires `cache.serve`, eligible membership, and a valid local
`nix-cache-signing-key`. Eligible producers are full members and the creator's
provisional control plane; pending nodes may consume caches but not serve them.
Do not generate signing keys automatically. Missing/invalid keys keep serving
offline (503), even for previously signed outputs, without disabling local builds.

Publish the selected key's public half through authenticated cluster metadata;
receivers trust that public key in Nix. Private keys stay on their configured holders.

| Operation | Requirement |
| --- | --- |
| Build on a selected host | Nix/build capability; no signing key required |
| Make its outputs reusable by peers | Serving ready and receivers trust its public signing key |
| Apply/install/write on a target | Verified output local to that target; no live remote builder required |

Build does not automatically run a second build elsewhere. Apply may first request
Build on another host, then runs the target's build of the same pinned source.
Record each build outcome separately; cache loss can cause target compilation, not
an output-transfer failure. A consumer unable to compile a recipe can substitute
it, but an unsupported-system cache miss must fail explicitly.

## Configuration and status

Defaults: gateway enabled, serving permitted, no signing credential, four concurrent
HTTP streams per direction. `cache.enable` controls the local gateway; `cache.serve`
controls serving independently. `cache.signingCredentialRef` selects the local key.
`cache.maxConcurrentTransfers` bounds incoming exports and outgoing pulls separately.

Derive the local substituter URL from `http.port`, preserve external substituters,
and install trusted public cache keys. Standalone agents use the same integration.

```sh
nixmox credentials import cache-signer --kind nix-cache-signing-key --node alder --file /run/keys/cache-key
nixmox cache credential set cache-signer
nixmox cache status --json
nixmox cache locate /nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-example
```

`GET /api/v1/cache/status` returns `node_id`, `version`, `observed_at`, `local_url`,
`gateway_state`, `serving`, `serving_reason`, `active_transfers`, and peer status.
Gateway state is `ready|disabled|unavailable`. Serving reasons are
`SERVING_DISABLED`, `MEMBERSHIP_INELIGIBLE`, `SIGNING_CREDENTIAL_REQUIRED`, or
`SIGNING_CREDENTIAL_UNAVAILABLE`; null means serving. Active transfers are HTTP streams.

`GET /api/v1/cache/locations?path=<store-path>` performs a bounded live lookup.
Browser/TUI Settings → Cache exposes status, credentials, peer health, and Locate.
Build progress reports actual sources, misses, and local fallback. Readiness changes
feed shared events/actions; OTel records lookups, bytes, failures, and cancellation.

## Embedded serving

Use pinned `go-nix/pkg/nar` inside the daemon, not a companion server or per-download
exporter subprocess. Start with uncompressed NARs: `Compression: none`, FileHash /
FileSize equal NarHash / NarSize. Interrupted requests restart; Range may return 200.

Use an indexed store-hash lookup refreshed after builds/imports/GC and external
store changes, not a whole-store scan per request. Preserve existing signatures
and add the selected key's signature using Nix's fingerprint. Hold a GC root while
serializing and scope failures to that request.

Verify split-peer substitution, streaming/cancellation/GC safety, signature rejection,
builder disappearance with local fallback, source pinning, unsupported-system
misses, changed-output reviews, and signing-key loss/recovery.
