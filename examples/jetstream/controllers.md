# Controller architecture sketch

This is back to stubs. The public walkthrough in `main.go` remains executable,
but its first unimplemented API call reports `errors.ErrUnsupported`.
Reusable low-level NATS functions remain implemented in `runtime/nats.go`.

## Layout

- `interface.go`: public daemon functions: create, enroll, promote, demote, evict, stop, close.
- `daemon.go`: internal construction, registration, startup and cleanup.
- `dispatcher.go`: one coalescing dispatch loop per host; no router or work queues.
- `stores/`: one store/cache/watch per bucket, plus separate task-event publisher/consumer.
- `records/`: record definitions and host-bound typed creation/update stubs.
- `controllers/`: controller-owned interests, quick Step methods and asynchronous executions.
- `runtime/`: low-level NATS manager, usable before stores/controllers exist.

## Store changes

1. Each bucket starts its own independent watch.
2. Its handler decodes the change and updates its local cache.
3. It calls `StoreChanged(storeID)`; repeated notifications set the same dirty flag.
4. One dispatcher atomically takes and clears dirty flags **before** capturing snapshots.
5. It invokes matching registered controllers using their declared store interests.
6. Steps inspect current state and initiate execution without waiting for IO.
7. If notifications arrived during the pass, the dispatcher repeats with fresh snapshots.
8. Otherwise it atomically marks itself idle and exits.

No event backlog is retained. Task controllers inspect current local tasks in the task
snapshot. Command tasks subscribe only to task-store changes, not membership changes.
Missing initial synchronization is distinct from an empty store.

Execution completion can directly mark its controller dirty, even without a KV update.
Controllers own in-flight tracking, cancellation and safe result delivery. Those
synchronization details remain comments—not another implemented framework.

## Commands

Task commands use NATS publishing and a durable host-filtered consumer. The consumer
invokes the task scheduler and acknowledges after desired state is persisted. These
events are not coalesced by the dispatcher.

## Low-level code retained

The NATS manager retains explicit primary/bootstrap methods and shared helpers for
server configuration, start/stop, status inspection, connections and teardown.
Eviction, metadata removal and shutdown authorization are outside this manager.
Stopping a server retains its storage. Conflicting manager calls must be serialized
by the caller.

Removed from the manager: controller status bookkeeping, mutex-based shared caches,
polling status notifications, and hardcoded bucket replication policy. Each store now
declares its own `UpdateReplicas`. Manager status observation is stubbed.

Cluster creation uses the manager directly first, provisions stores, creates local
membership, then attaches watches/controllers. Enrollment opens existing stores and
creates client-only full membership.

Fixed APP/SYS credentials and loopback listeners in the retained manager are for this
example only.
