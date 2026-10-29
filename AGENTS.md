KEEP IMPLEMENTATION AS CLEAN AND CONDENSED AS YOU CAN
KEEP LINES OF CODE AS LOW AS POSSIBLE.
FAVOR CLEAN ABSTRACTIONS WITH GOOD REUSE.
DO NOT SACRIFICE READABILITY TO REDUCE LINE COUNT.
READ LIBRARY DOCS AND FOLLOW THEIR CONVENTIONS. NEVER HACK AROUND LIBRARY BEST PRACTICES WITHOUT CONSULTING FIRST.
DO NOT BUILD CUSTOM FRAMEWORKS THAT DUPLICATE THE LIBRARIES WE USE.
KEEP RESPONSIBILITIES CLEAR, FUNCTIONS FOCUSED, AND NAMES DESCRIPTIVE.
Do not write readme.md files until the end once the application reaches maturity.
## Disposable test machine
validate real functions that can't be virtualized on the test machine. 
This would be things such as media, kexec, disk partitioning, wifi testing etc.

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
DO NOT MAKE UNIT TESTS THEY ARE TOO BRITTLE. 
ONLY TESTS ALLOWED ARE 
  - USAGE TESTS THAT SHOW CLEAR STATIC FUNCTION INPUT AND STATIC FUNCTION OUTPUT
  - SIMPLE INTEGRATION TESTS THAT CONFIRM DOCUMENTED FUNCTIONALITY
STOP EXPANDING SCOPE WHEN A COMPLEX PROBLEM IS ENCOUNTERED, DEFER TO USER DECISION
RECORD FEATURE PROGRESS AND BLOCKERS IN A PROGRESS.md FILE.
BLOCKER SHOULD BE RECORDED IF IT IS NOT SOLVED AFTER AN IMMEDIATE FIX
PROGRESS ENTRIES SHOULD BE SINGLE LINE ENTRIES WITH START TIMESTAMP
IF A BLOCKER HAS TAKEN OVER 5MIN THEN STOP. REPORT THE BLOCKER TO USER WITH WHAT WAS TRIED AND ALTERNATIVE APPROACHES TO TAKE
MOVE ON TO NEXT FEATURE UNTIL DESIRED USER GOAL REACHED OR HARD BLOCKER ENCOUNTERED

## Worksace Details

Create a flake and use its devshell for any tooling needeed
Use just files to run services/tests. Ensure it is easy for User to Execute

## Git workflow

- The agent may commit and push changes on development branches.
- Create or switch to a development branch before committing.
- Never commit or push directly to `main`, merge into `main`, or force-push
  without explicit authorization.
- Do not include unrelated changes or credentials in commits.
