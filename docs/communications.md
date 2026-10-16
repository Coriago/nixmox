# Command and live-state transport

Nixmox uses NATS for application communication between agents, while browsers
continue to use the HTTP API and a single SSE connection. The embedded NATS
client port remains on loopback. A browser does not receive broker credentials
or direct access to JetStream administration.

## Commands have one path

Every production HTTP mutation enters `nixmox_commands` before its handler runs,
including a command addressed to the node serving the API. The envelope records
an ID, destination, method, path, content type, body, and expiry. Local and remote
commands use the same durable per-host consumer, acknowledgement and receipt
logic. There is no direct-execution fallback when NATS is unavailable. Rollout
and automatic GitOps apply use this same transport; queued jobs retain their
existing per-host FIFO executor.

The stream uses file storage, explicit consumer acknowledgements, a ten-minute
broker deduplication window, delivery backoff, and bounded in-flight deliveries.
It retains command envelopes for up to 30 days / 64 MiB. Discard-new backpressure
prevents silently evicting queued commands when full. The stream promotes from
one replica to three alongside cluster metadata, never two. It only scales down
through graceful removal; loss of quorum must not permit local execution.

The HTTP response includes `X-Nixmox-Command-ID`. Clients can supply a stable
`Idempotency-Key` to retry the same request. After execution is admitted, the
normal handler response is returned. A transport timeout returns an error with
the command ID and instructions to retry with that same key: timeout does not
prove rejection. Retrieve the durable target receipt at
`GET /api/v1/hosts/<host>/commands/<id>`. Command admission and job completion are
separate; a receipt containing an accepted job does not claim deployment success.

Receipts are atomically persisted on the executing host, independently of NATS
cluster topology. Duplicate delivery returns the existing response, including
after daemon restart. Reusing an ID with different request contents is rejected.
Receipt files retain duplicate protection beyond the broker's deduplication window.
They contain a request fingerprint and result rather than another copy of the
command body. Full job logs and application history remain in the existing local
stores.

## Retry boundaries

JetStream retries message delivery and acknowledgement loss. Failures before
persisting the execution receipt are negatively acknowledged with delay. A
receipt is written before invoking effects and completed before acknowledgement.
A redelivery of a completed command returns its recorded result. A receipt left
executing after a process interruption becomes an explicit interrupted outcome;
it never blindly repeats arbitrary shell commands, USB writes or activation.
This is duplicate protection, not a claim of exactly-once external effects.

Commands expire after two minutes if not started. Expired deliveries receive a
terminal receipt rather than executing unexpectedly when a host returns much
later. Validation failures are terminal responses, not retry loops. Running
handlers survive a NATS connection generation change; their execution lifetime
belongs to the daemon, so a join or leave can complete through its own broker
restart. Jobs already accepted into the persisted executor retain their existing
restart rules, including fresh confirmation for interrupted USB writes.

## Reads and live updates

Peer reads use NATS request/reply. Large responses such as logs are chunked into
64 KiB messages, with a 64 MiB response bound; oversized downloads fail explicitly
rather than silently truncating. HTTP endpoints remain available as the browser
and CLI interface, but peer task/history/status requests no longer open HTTP
connections to other clustered hosts.

Job creation, state/phase/progress changes, and Git state changes
signal a coalescing publisher. It writes the latest compact host snapshot to the
existing JetStream `hosts` KV bucket, including active deployment metadata. Logs
and large package/configuration previews are not duplicated into heartbeats.
Watch notifications update each node's inventory and wake its browser SSE
streams. Fragments are refreshed on these events and only changed HTML is sent.
Task metadata is fetched over NATS when its subscribed view is refreshed.
Log bytes have a separate incremental path: after an append succeeds, the host
publishes bounded chunks on `nixmox.logs.<encoded-host>.<encoded-job>`. The gateway
subscribes before reading the initial persisted tail, then sends `log` SSE events
with absolute byte offsets. Duplicate chunks are ignored; gaps trigger bounded
catch-up reads over NATS. The safety timer recovers a dropped final chunk even
when no later output arrives. Broker generation changes close the browser stream
so reconnection installs a new subscription. Browser output uses a streaming UTF-8
decoder and text nodes, preserving split characters without interpreting log HTML.
The displayed tail is bounded; the complete saved log remains downloadable.
Appending log output no longer republishes a host snapshot for every line.
The 10-second heartbeat and safety refresh remain for membership expiry and
recovery; deployment responsiveness no longer waits for them. Reconnect sends a
fresh snapshot instead of depending on missed transient notifications.

Hardware telemetry has a separate two-second sampler and NATS Core subject,
`nixmox.telemetry.<encoded-host>`. CPU, memory, load, disk I/O counters and interface bandwidth
are refreshed on that cadence, independently of task traffic. Network and disk
rates use counter deltas and actual elapsed time; first samples and reset counters
show a sampling state rather than false spikes. CPU identity, NIC/address inventory,
drives, mounts and local filesystem capacity are collected every 30 seconds.
Fast messages omit slow inventory contents; the receiver merges them with its
last inventory. Full inventory accompanies its refresh and is also available in
the retained host snapshot for new watchers. Collection is shared across viewers.
Network/FUSE filesystem capacity is excluded to avoid blocking local telemetry
on an unavailable remote mount. These replaceable
samples do not need JetStream replay and do not renew membership liveness. A
missed sample is replaced by the next one; host snapshots provide initial state.
Sampling is shared across viewers, so opening more browser tabs does not multiply
hardware collection. SSE sends the changed telemetry fragments as samples arrive.

Uptime is a one-second browser clock anchored to the latest host report. Replacing
HTML fragments restores that clock instead of advancing it on each event. Offline
hosts and disconnected browsers freeze the display; reboot reports reset it.
This removes bursty uptime changes without increasing network traffic.

The console prominently reports loss of its live connection, retains stale data,
and reconnects automatically. Named SSE keepalives let a 20-second watchdog detect
silent proxy/network stalls as well as explicit connection errors. A reconnect
button retries immediately. Links to other previously online hosts use their
advertised addresses; the console does not silently switch origins, downgrade a
remote HTTPS session, resend commands to another host, or assume LAN addresses
are browser-reachable. A reverse proxy with a stable URL is the appropriate place
for transparent gateway failover.

The browser retains the same command ID for an identical action whose response
is unknown, including network errors. This protection lasts for the page's
lifetime; API clients should persist their keys across reloads. Known responses
clear the pending key so a subsequent deliberate action gets a new command.
Receipt persistence syncs both file contents and the containing directory.

## Topology boundaries

Unjoined nodes have separate NATS systems, so discovery/enrollment cannot assume
an existing cluster route. Enrollment and graceful removal retain an HTTP
control-plane bridge while routes are being established or torn down. The
receiving API still submits the mutation through its local JetStream consumer.
Join can recover its completed local receipt while its broker is restarting.
Remote leave deliberately severs the NATS route carrying its result, so its
caller also checks the completed receipt over peer HTTP. That exception only
recovers an already executed result; it never bypasses NATS command admission.
Discovery uses LAN discovery; Git, package fetching, SSH provisioning and USB
block writes continue using their native protocols. A browser connects only to
nixmox; this change does not expose a NATS WebSocket listener.

## Verification

The tests use real embedded NATS/JetStream for local/remote commands, retained
stream history, duplicate IDs, consumer interruption/rearming, expired commands,
uncertain execution recovery, complete chunked reads and unavailable transport.
The comprehensive three-VM E2E verifies command receipts, retry/restart duplicate
protection, and remote work with peer HTTP blocked. It includes command-stream
replication in the existing quorum loss/recovery and graceful removal checks,
and watches an already-open SSE connection update deployment state in both the
host table and sidebar. Both E2E parts remain the final acceptance gate.

Browser acceptance in the same three-VM run checks one-second uptime through busy
and quiet periods, offline/reboot clocks, split fast/slow remote telemetry, incremental log catch-up, visible
connection loss/recovery, alternative-host links, and desktop/mobile layout.

## Review and design references

The final simplification pass reused the same NATS handler path for local and
remote commands, separated fast activity from slow inventory, removed repeated
log-fragment downloads, and kept rendering in shared templates. Response buffers
are bounded on both sides of the transport. Command result subjects include the
request fingerprint so concurrent misuse of an idempotency key cannot return
another request's response. Remote job pages render through the serving gateway,
keeping the “This UI” marker correct while their task data comes from another host.

Core NATS is intentionally ephemeral; telemetry is superseded by the next sample
and log gaps are recovered from persisted bytes. Durable commands use JetStream.
This follows the distinction in the [NATS Core documentation](https://docs.nats.io/learn/core-nats/).
Disk rates use Linux sector counters and handle resets described in the
[kernel I/O statistics documentation](https://docs.kernel.org/admin-guide/iostats.html).
The GitOps view separates revision sync from actual package/configuration preview,
consistent with the importance of explaining differences in
[Argo CD's diff model](https://argo-cd.readthedocs.io/en/stable/user-guide/diffing/).
