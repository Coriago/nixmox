# Eviction scenario

A manages its own eviction task and currently hosts bootstrap. The bucket has `dataReplicas = 2`. B's primary is running, but has no replica:

```text
A primary:   replica
A bootstrap: replica
B primary:   empty
B bootstrap: absent
```

## 1. A requests eviction

A's `taskSchedulerController` receives the task event and writes the A-owned task's desired state.

A's `membershipTaskController` then sets:

```text
A.desired.evict = true
```

This requests primary replica eviction and makes A ineligible for bootstrap placement. It does not request primary shutdown or metadata removal.

## 2. A begins primary eviction

A's `natsServiceController` asynchronously attempts to move replicas off A's primary.

It may move the primary's replica to B primary before B bootstrap starts:

```text
A primary:   empty
A bootstrap: replica
B primary:   replica
B bootstrap: absent
```

Track eviction separately from whether the server is running. Use these values for `state.primary.evict` and `state.bootstrap.evict`:

- `pending`: requested, but no attempt has started.
- `running`: an eviction attempt is in progress.
- `attempted`: the attempt finished, but replicas remain because current topology cannot accommodate them.
- `completed`: inspection confirms no data replicas remain on that server.
- `failed`: the attempt encountered an error; record the error separately.

Include the eviction operation ID and remaining replica count with the result. An old `completed` result must not satisfy a new request.

If the primary's replica successfully moves, A reports:

```text
A.state.primary.evict = completed
A.state.primary.status = ready
```

If it cannot move yet, A reports `attempted` with the remaining replica count. Relevant topology changes, such as B bootstrap becoming ready, enqueue another attempt; the controller does not busy-loop.

This ordering is possible, not guaranteed. The task does not complete yet.

## 3. B starts bootstrap independently

Bootstrap controllers observe A's eviction intent and independently determine B is the replacement. Only B writes:

```text
B.desired.bootstrap = true
```

B's `natsServiceController` starts bootstrap asynchronously and publishes readiness.

A retains its existing bootstrap while waiting for B to become ready.

## 4. A requests bootstrap shutdown

A's `bootstrapController` observes B's readiness and sets:

```text
A.desired.bootstrap = false
```

## 5. A evicts bootstrap replicas before stopping it

A's `natsServiceController` performs a server-specific eviction attempt before shutting down A bootstrap.

In the ordering above, its replica can now move to B bootstrap:

```text
A primary:   empty, running
A bootstrap: empty, then stopped
B primary:   replica
B bootstrap: replica
```

A reports:

```text
A.state.bootstrap.evict = completed
A.state.bootstrap.status = shutdown
```

If the asynchronous operations happen in a different order, controllers track actual replica movement rather than assuming these destinations.

## 6. A waits for both paths before completing

A's `membershipTaskController` checks:

```text
A.state.primary.evict == completed
A.state.bootstrap.evict == completed
A.state.bootstrap.status == shutdown
required replacement bootstrap is ready
```

All results must match the current operation. This successful full-evacuation scenario requires `completed` for both servers. `attempted` does not satisfy that gate: the task remains waiting with the remaining replicas visible.

A shutdown's pre-stop eviction can also finish as `attempted`. That means eviction was tried, not that shutdown is safe. The shutdown/handover safety checks decide whether stopping may proceed; the eviction status must not be changed to `completed` merely because the process stopped.

The completed state on A is:

```yaml
state:
  bootstrap:
    evict: completed
    status: shutdown
  primary:
    evict: completed
    status: ready
```

A's primary remains running and remains a metadata member. Eviction alone does not remove or stop it.
