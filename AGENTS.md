## Mandatory

   Before running any project inspection, tooling, or implementation:
   1. Read the root AGENTS.md and existing PROGRESS.md, if present.
   2. Create PROGRESS.md if absent.
   3. Append a timestamped START entry describing the user's task.
   4. Add an entry for what you will do next given the task
   5. Only then begin investigation or implementation.

RECORD FEATURE PROGRESS AND BLOCKERS IN A PROGRESS.md FILE.
BLOCKER SHOULD BE RECORDED IF IT IS NOT SOLVED AFTER AN IMMEDIATE FIX
PROGRESS ENTRIES SHOULD BE SINGLE LINE, SHORT, AND CONTAIN A START TIMESTAMP 
IF A BLOCKER HAS TAKEN OVER 5MIN THEN STOP. REPORT THE BLOCKER TO USER WITH WHAT WAS TRIED AND ALTERNATIVE APPROACHES TO TAKE
MOVE ON TO NEXT FEATURE UNTIL DESIRED USER GOAL REACHED OR HARD BLOCKER ENCOUNTERED

## Mandatory Guidlines
Before declaring a change complete, review the affected workflow end to end—not just
 edited lines. Check surrounding code, callers, ownership boundaries, naming, readability,
 duplication, tests, and documentation for consistency.
FAVOR CLEAN ABSTRACTIONS WITH GOOD REUSE.
DO NOT SACRIFICE READABILITY TO REDUCE LINE COUNT.
READ LIBRARY DOCS AND FOLLOW THEIR CONVENTIONS. NEVER HACK AROUND LIBRARY BEST PRACTICES WITHOUT CONSULTING FIRST.
DO NOT BUILD CUSTOM FRAMEWORKS THAT DUPLICATE THE LIBRARIES WE USE.
KEEP RESPONSIBILITIES CLEAR, FUNCTIONS FOCUSED, AND NAMES DESCRIPTIVE.
STOP EXPANDING SCOPE WHEN A COMPLEX PROBLEM IS ENCOUNTERED, DEFER TO USER DECISION
Do not write readme.md files until the end once the application reaches maturity.
No tables in markdown files

## Mandatory Guide for Tests
DO NOT MAKE EXTENSIVE UNIT TESTS THEY ARE TOO BRITTLE. 
ONLY WRITE DOCUMENTATION-STYLE USAGE TESTS.
 - USE Go Example functions WITH // Output: TO ASSERT EXACT OUTPUT.
 - SHOW MAIN USAGE, NOT EVERY POSSIBLE INPUT.
 - SEPARATE SETUP, USAGE, AND ASSERTIONS WITH CLEAR COMMENT BOUNDARIES.
 - KEEP OUTPUT DETERMINISTIC. RESTORE CHANGED GLOBAL STATE.
```go
func ExampleThing() {
    // --- Test setup ---
    // ------------------
    //
    setup code here
    //
    // --- Usage ---
    // -------------
    //
    clear code example here
    //
    // --- Assertions ---
    // ------------------
    //
    // Output:
}
```
WRITE SIMPLE INTEGRATION TESTS THAT CONFIRM DOCUMENTED FUNCTIONALITY/BEHAVIOR FOR HIGH LEVEL COMPONENTS ONLY

## Worksace Guide

Create a flake and use its devshell for any tooling needeed
Use just files to run services/tests. Ensure it is easy for User to Execute

## Git workflow

- The agent may commit and push changes on development branches.
- Create or switch to a development branch before committing.
- Never commit or push directly to `main`, merge into `main`, or force-push
  without explicit authorization.
- Do not include unrelated changes or credentials in commits.

## Disposable test machine
validate real functions that can't be virtualized on the test machine. 
This would be things such as media, kexec, disk partitioning, wifi testing etc.

- SSH: `root@heliosmac.local`.
- Network: Wi-Fi only; preserve Wi-Fi credentials and connection configuration.
- The test machine and this USB may be wiped and reinstalled. This authorization
  does not cover the developer host or other devices.
- Before reboot or kexec, preserve SSH access and required Nixmox identity/trust;
  verify the destination has Wi-Fi drivers/firmware, credentials, network setup,
  and SSH service. Verify remote access after each transition.
- Establish a recovery path before a transition that could lose access. No
  independent remote console or power control has been specified.
- Reboot and kexec are authorized for testing. Do not power off the machine
  without a verified way to power it back on remotely.
