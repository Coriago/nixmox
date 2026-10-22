specification documents are in docs/
Specifications must remain concise and focus on key details.
Do not get verbose
Favor examples of usage, configuration and data objects over verbose explanations.

Implement the rewrite specified in docs/.
Do not write readme.md files until the end once the application reaches maturity.

## Sources of truth

- specs define intended behavior, interfaces, and defaults.
- If specs conflict or omit a consequential decision, identify it before
  implementing dependent behavior. Resolve routine implementation details yourself.

## Implementation approach

- Deliver small, complete vertical slices with working integration and tests.
- Use one domain/service layer across CLI, browser, and TUI.
- Keep transport, rendering, persistence, and external-tool adapters separate
  from domain rules.
- Follow the specified command admission, task lifecycle, concurrency,
  credential, and storage contracts.
- Do not introduce compatibility layers, new frameworks, or additional services
  without a concrete requirement.
- Always consider refactoring code after each deliverable to keep the code base clean.

## Development environment

- Create a project `flake.nix` with a default development shell and pin inputs
  in `flake.lock`.
- Add any needed development, build, and test tooling to the dev shell; do not
  install tooling globally or modify the developer host's system configuration.
- Run tooling through `nix develop`, or `nix develop -c <command>` for automation.

## Correctness

- Never bypass approval, validation, membership permissions, or quorum
  requirements to make a workflow succeed.
- Preserve pinned source/output identity and command retry identity.
- Reconcile uncertain effects; never blindly repeat destructive operations.
- Keep secrets out of Git, the Nix store, command payloads, logs, and diagnostics.
- Bound queues, buffers, retries, and external calls; support cancellation and
  explicit unavailable/stale states.

## Verification

- Use the acceptance matrix in docs/rewrite/technical-requirements.md.
- Run checks appropriate to each change, including real integration tests at
  affected Nix, NATS, Git, and operating-system boundaries.
- Distributed claims require multi-node tests, including quorum failure and recovery.
- Boot/media support requires disposable physical-device evidence.
- Never run destructive tests on the developer host or non-disposable devices.
- Do not claim a requirement is complete based only on mocks or documentation.

## Completion

- Finish the authorized slice rather than leaving disconnected scaffolding.
- Report what works, what was tested, and remaining limitations or gates.
- Distinguish implemented behavior from planned or unverified behavior.

## Disposable test machine

- SSH: `root@heliosmac.local`.
- Network: Wi-Fi only; preserve Wi-Fi credentials and connection configuration.
- Authorized bootable USB: `/dev/sdc`, reported capacity 115.5 GB, on the test host.
- The test machine and this USB may be wiped and reinstalled. This authorization
  does not cover the developer host or other devices.
- Before erasing, verify the remote host and device identity, capacity, and stable
  `/dev/disk/by-id/` path. Device names such as `/dev/sdc` can change after reboot.
- Before reboot or kexec, preserve SSH access and required Nixmox identity/trust;
  verify the destination has Wi-Fi drivers/firmware, credentials, network setup,
  and SSH service. Verify remote access after each transition.
- Establish a recovery path before a transition that could lose access. No
  independent remote console or power control has been specified.
- Reboot and kexec are authorized for testing. Do not power off the machine
  without a verified way to power it back on remotely.

## Git workflow

- The agent may commit and push changes on development branches.
- Create or switch to a development branch before committing.
- Never commit or push directly to `main`, merge into `main`, or force-push
  without explicit authorization.
- Do not include unrelated changes or credentials in commits.
