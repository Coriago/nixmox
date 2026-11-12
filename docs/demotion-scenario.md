# Demotion scenario

A is being demoted from full-stable to full. A manages its own demotion task. B is the deterministic replacement bootstrap host.

## 1. A receives the demotion request

A's `taskSchedulerController` receives the task event and writes the A-owned task's desired state.

## 2. A publishes demotion intent

A's `membershipTaskController` sets:

```text
A.desired.role = full
A.desired.evict = true
```

The task waits for primary eviction and bootstrap handover. A's actual role remains full-stable and its primary remains running.

## 3. Primary eviction begins

A's `natsServiceController` sees `desired.evict` and asynchronously attempts to move replicas off A's primary wherever topology permits.

It tracks the result under `A.state.primary.evict`, separately from the server's runtime status:

- `pending`: requested, but not started.
- `running`: an attempt is in progress.
- `attempted`: the attempt finished, but replicas remain because topology cannot accommodate them.
- `completed`: inspection confirms no data replicas remain.
- `failed`: an error prevented the attempt from completing; record the error separately.

The result includes the current eviction operation ID and remaining replica count. Relevant topology changes enqueue another attempt when replicas remain.

These same values apply to `state.bootstrap.evict`. Eviction does not remove the primary from metadata membership or stop it.

## 4. B starts bootstrap

Bootstrap controllers independently observe the topology and A's eviction intent. All determine B should host bootstrap; only B writes:

```text
B.desired.bootstrap = true
```

B's `natsServiceController` starts bootstrap asynchronously and publishes readiness.

Primary eviction may progress before B bootstrap is ready; these are independent operations.

## 5. A hands over its existing bootstrap

If A currently hosts bootstrap, A's `bootstrapController` retains it until B is ready, then sets:

```text
A.desired.bootstrap = false
```

A's `natsServiceController` attempts to evict replicas from that bootstrap before stopping it, then reports:

```text
A.state.bootstrap.evict = completed
A.state.bootstrap.status = shutdown
```

This step is not applicable if A did not host bootstrap.

## 6. A waits for both paths

A's `membershipTaskController` observes membership updates and verifies:

- Primary replica eviction is `completed`, meaning no replicas remain, for this successful evacuation path.
- B's required replacement bootstrap is ready.
- A's outgoing bootstrap has completed eviction and shutdown, if applicable.

Results must belong to the current operation, not an earlier eviction or startup. `attempted` is not equivalent to `completed`: remaining replicas stay visible and this full-evacuation path remains waiting. Any policy allowing progress with remaining replicas must explicitly check safety rather than treating an attempted eviction as successful evacuation.

## 7. A requests primary shutdown

A's `membershipTaskController` sets:

```text
A.desired.primary = false
```

A's `natsServiceController` performs another server-specific eviction attempt before shutdown. The demotion retirement procedure also removes primary metadata membership and ensures client connectivity to a surviving server before stopping the primary. Metadata removal is separate from eviction. A pre-stop eviction result of `attempted` does not itself authorize shutdown; retirement safety checks still apply. Stopping a process must not turn its eviction result into `completed`.

## 8. A completes the task

After observing primary shutdown, A's `membershipTaskController` finalizes the role as full and marks the task complete.

A retains cache hosting and full permissions.
