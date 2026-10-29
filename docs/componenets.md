# Daemon components and flow

Feature requirements: [betterRewrite.md](betterRewrite.md). CLI/API names below are proposed interfaces, not an implementation inventory. API paths are relative to `/api/v1`; CLI commands start with `nixmox`.

## rules
- CLI/browser → HTTP API → owning component → NATS. No universal operation dispatcher.
- NATS owns cluster records: fetch once per operation and pass records through functions; do not maintain duplicate catalogs.
- Keep local runtime state and identity needed before connecting. Cache peers/signing configuration stay local to avoid lookups while serving artifacts.
- Watchers trigger service changes; consumers process durable work; pollers sample or fetch periodically.
- `BUCKET / key` means KV; Object Store entries are explicitly marked. IDs in subjects are single subject-safe tokens.
- Durable handlers tolerate redelivery and acknowledge after their work/state is persisted. Concurrent record updates use revision checks.

## local runtime and interfaces

### daemon
```yaml
configuration: {}
identity: { nodeId: node1, tlsKeyFile: /var/lib/nixmox/node.key }
membership: { status: unjoined, clusterId: null, caFile: null, endpoints: [] }
components: {}
```
- initialize(): `serve` loads local state, starts HTTP/discovery, then enrolled components.
- startCluster()/applyRole(): enrollment/membership invokes these to start/stop role-specific services and persist local membership.
- No owned NATS subjects or poller.
- CLI: `serve` starts the daemon locally, not through HTTP.

### HTTP server
```yaml
listener: null
router: null
```
- initialize(): serve `/api/v1`, browser `/`, SSE and cache routes on one listener.
- Handlers call component functions below; reads fetch records, mutations write records or publish work as specified by the component.
- No separate API subject namespace or background subscriptions.

### discovery service
```yaml
advertisement: {}
discovered: {}
socket: null
```
- initialize(): start discovery listener and periodic broadcasts; no NATS dependency.
- list(): discovery API reads discovered endpoints.
- setAdvertisement(): membership manager updates advertised identity/endpoints.
- `discover` → `GET /discovery` → list().

### interface stream hub
```yaml
viewers: {}
feeds: {}
replayPositions: {}
```
- initialize(): empty maps; no feeds until requested.
- attach()/detach(): SSE handlers share live subscriptions, KV watches and log replay consumers; release unused feeds.
- Reads task/host snapshots before following live updates; uses subjects/stores owned below.
- `task logs <id> --follow` → `GET /tasks/<id>/logs` → TASK_LOGS replay/follow.
- `watch` → `GET /events` → requested telemetry, progress and record-change feeds over SSE.

## cluster connectivity and membership

### NATS runtime
```yaml
client: null
server: null
bootstrapServer: null
endpoints: []
tlsConfiguration: {}
storeDirectory: /var/lib/nixmox/nats
```
- initialize(): load TLS identity; full members start embedded servers, then connect locally; pending members connect to a full member.
- Full members provision stores/streams; pending members open permitted existing stores.
- connect(), startServer()/stopServer(): daemon invokes on startup/role changes.
- startBootstrap()/stopBootstrap(): membership manager invokes on assignment changes; bootstrap has separate identity/storage.
- Exposes native NATS APIs, not an application router. No direct CLI/API surface.

### membership manager
```yaml
nodeId: node1
operationConsumer: null
memberWatcher: null
bootstrapWatcher: null
```
- initialize(): watch MEMBERS and CLUSTER/bootstrap; start durable membership consumer, filtered to all targets on full members or own node on pending members.
- requestPromotion()/requestDemotion()/requestRemoval(): publish `nixmox.membership.request.<nodeId>` → MEMBERSHIP_OPERATIONS.
- Consumer performs this host's assigned step: daemon applyRole(), credential recipient changes or native replica/peer changes. Each participant has its own consumer.
- Member watcher calls cache addPeer()/removePeer() and discovery setAdvertisement(); bootstrap watcher reconciles the assigned server.
- list()/get(): fetch records, without a local member map.
- KV: `MEMBERS / <nodeId>`, `CLUSTER / identity`, `CLUSTER / bootstrap`, `ENROLLMENT / <requestId>`, `REMOVALS / <nodeId>`.
- `member list` → `GET /members` → list().
- `member promote|demote|remove <id>` → `POST /members/<id>/promote|demote|remove` → matching request function.
- Initial enrollment and force-removal checks use HTTP, not NATS subjects.

## credentials and repositories

### credential store
```yaml
ageIdentityFile: <local SSH-derived age identity>
watcher: null
```
- initialize(): full members load decryption identity; watch only credentials actively used by services and notify consumers to reload.
- get(): cache, Git and enrollment signing fetch/decrypt credentials on demand.
- put()/delete(): encrypt/write or delete `CREDENTIALS / <credentialId>`; do not publish plaintext credentials in task messages.
- addRecipient()/removeRecipient(): designated full member's membership manager invokes SOPS recipient updates.
- `credential set|delete <id>` → `PUT|DELETE /credentials/<id>` → put()/delete() on a full member.
- Public API exposes descriptors, not internal get() plaintext results.

### Git repository manager
```yaml
checkouts: {}
pollInterval: 60s
poller: null
```
- initialize(): discover local checkouts; full members start polling. Each pass fetches `REPOSITORIES / <repoId>` records, not a cached list.
- fetch(): poller/task obtains credentials, fetches refs and publishes `nixmox.git.upstream.<repoId>` → GIT_UPDATES.
- Payload carries ref and old/new SHA. Wait for publish acknowledgement before recording announcement state; duplicate notifications remain possible.
- checkout(): collection/build/publication runners obtain pinned sources.
- publish(): publication runner pushes changes or opens a pull request.
- add()/remove(): repository API updates KV directly.
- `repo add|remove` → `POST /repositories` or `DELETE /repositories/<id>` → add()/remove().
- `repo fetch <id>` → `POST /repositories/<id>/fetch` → task manager → fetch().
- Pending members may fetch public sources on demand; private repository access remains deferred.

### configuration registry
```yaml
upstreamConsumer: null
```
- initialize(): full members consume GIT_UPDATES (`nixmox.git.upstream.*`); no background recipe/configuration maps or watchers.
- refreshCatalog(): consumer checks out the announced revision and stores discovered records; repeated notifications are safe.
- collect(): fetch recipe once, use membership list()/Git checkout()/host observation tasks, save a new version.
- render(): build/publication runners render a saved version on demand.
- list()/get(): fetch `CONFIGURATIONS / <configurationId>` or `RECIPES / <recipeId>`.
- Object Store: `RECIPE_VERSIONS / <recipeId>/<version>` holds complete immutable versions; RECIPES contains descriptors/version references.
- `config list` → `GET /configurations` → list().
- `recipe show <id>` → `GET /recipes/<id>` → get().
- `recipe collect|publish <id>` → `POST /recipes/<id>/collect|publish` → task manager → collect() or render() + Git publish().

## tasks and telemetry

### task manager
```yaml
runners: {}
taskConsumer: null
controlConsumer: null
cleanupWorker: null
```
- initialize(): durable host-filtered consumers for TASK_REQUESTS and TASK_CONTROLS; reconcile local runners; full members start infrequent offline-host cleanup.
- submit(): publish `nixmox.task.submit.<nodeId>` → TASK_REQUESTS.
- cancel()/approve(): publish `nixmox.task.control.<nodeId>.<taskId>` → TASK_CONTROLS.
- handleTask()/handleControl(): consumers reconcile local state, start runners or apply controls; redelivery must not duplicate execution.
- list()/get(): reconcile local running tasks when invoked and fetch records. No periodic local runner check.
- cleanup(): mark tasks unknown after their host's offline interval.
- KV: `TASKS / <nodeId>.<taskId>` holds latest state, results and parent/retry links; completed records remain for lookup.
- Object Store: `TASK_OUTPUTS / <taskId>/<outputName>` holds review output/attachments referenced by task records.
- `task list|show` → `GET /tasks` or `GET /tasks/<id>` → list()/get().
- `task cancel|approve <id>` → `POST /tasks/<id>/cancel|approve` → cancel()/approve().
- `task retry <id>` → `POST /tasks/<id>/retry` → submit() with a new task id linked to the original.
- Task type, inputs and host assignment are payload fields, not separate subjects per operation.

### task runner
```yaml
 taskId: task1
 execution: null
 logAdapter: null
 progress: {}
 review: null
```
- initialize(): task manager loads inputs, starts task-specific execution and attaches a log adapter.
- cancel()/approve(): task manager controls execution. setReview(): execution saves review output and waits for approval.
- Stream: `nixmox.task.log.<nodeId>.<taskId>` → TASK_LOGS; full log replay, five-day retention.
- Core: `nixmox.task.progress.<nodeId>.<taskId>` → live progress.
- KV: `TASK_PROGRESS / <nodeId>.<taskId>` saves periodic/final progress; TASKS saves status/results.
- No independent task consumer or direct CLI/API surface; all controls go through task manager.

### telemetry collector
```yaml
latest: {}
sampleInterval: <configured interval>
worker: null
```
- initialize(): enrolled members start periodic sample(); no subscriptions.
- Core: `nixmox.host.telemetry.<nodeId>` and `nixmox.host.health.<nodeId>` publish live measurements.
- KV: `HOST_STATUS / <nodeId>` saves health/last-seen for initial views and offline-task cleanup.
- CLI/API live viewing uses interface stream hub; no full telemetry history retained here.

## cache

### cache manager
```yaml
peers: {}
enabled: false
signingKey: null
localStore: null
proxy: null
```
- initialize(): full members obtain signing credential and initial membership peers, then enable HTTP handlers.
- addPeer()/removePeer(): membership watcher updates local peer list.
- reloadSigningKey(): credential watcher triggers refresh. stop(): daemon disables handlers on demotion/destruction.
- serveLocal()/serveProxy(): HTTP uses local signing/peer state; no per-request NATS lookup or cache-specific subjects.
- `GET /store-cache/*` → serveLocal(); `GET /cache/*` → serveProxy(). These are Nix cache endpoints, outside `/api/v1`.

## daemon flow
- `serve`: restore local identity/membership, start HTTP and discovery; network setup, creation and enrollment work without NATS.
- Full: start embedded JetStream and assigned bootstrap server. Enrolled: connect NATS client.
- Start membership/task consumers and membership/bootstrap watchers; start telemetry sampling.
- Full only: initialize credentials, Git polling, catalog consumer and global task cleanup; enable cache after signing credentials/peers are ready.
- On demand: runners, recipe collection/rendering, Git operations and interface subscriptions/replay.
- Enrollment starts pending components; promotion adds full-member services; demotion stops them and reconnects to a surviving full member.
- Destruction stops cluster components, leaving local HTTP/discovery available.

## stream retention and native subjects
- MEMBERSHIP_OPERATIONS: `nixmox.membership.request.*`; Limits retention, independent participant consumers.
- GIT_UPDATES: `nixmox.git.upstream.*`; Limits retention, catalog consumers.
- TASK_REQUESTS: `nixmox.task.submit.*`; WorkQueue retention, one host-filtered consumer per host.
- TASK_CONTROLS: `nixmox.task.control.*.*`; WorkQueue retention, one host-filtered consumer per host.
- TASK_LOGS: `nixmox.task.log.*.*`; Limits retention, five days, independent replay consumers.
- Streams are shared across hosts/tasks. KV/Object Store use native APIs and their own backing streams; replication follows the feature guide's quorum policy.
- Native storage subjects: `$KV.<bucket>.<key>`, `$O.<bucket>.>`; native client subjects: scoped `$JS.API.>`, `$JS.ACK.>` and reply inboxes.
- System-account administration: `$JS.API.META.RESCUE` for disaster recovery, `$JS.API.SERVER.REMOVE` for metadata peer retirement.
