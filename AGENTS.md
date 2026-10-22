Implement the rewrite specified in docs/.
DO ALL IMPLEMENTATION IN ONE SHOT. NO UNIT TESTS. KEEP LINES OF CODE AS LOW AS POSSIBLE. FAVOR CLEAN ABSTRACTIONS WITH GOOD REUSE.
ONCE IMPLEMENTATION IS DONE MAKE SOME INTEGRATION TESTS
ONCE INTEGRATION TESTS ARE DONE DO E2E WITH VMS
VALIDATE REAL FUNCTIONS THAT CAN'T BE VIRTUALIZED ON THE TEST MACHINE. MEDIA, KEXEC ETC
Do not write readme.md files until the end once the application reaches maturity.

specs define intended behavior, interfaces, and defaults.
If specs conflict or omit a consequential decision, identify it before
  implementing dependent behavior. Resolve routine implementation details yourself.
Create a flake and use its devshell for any tooling you need.

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
