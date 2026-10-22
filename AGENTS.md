Implement the rewrite specified in docs/.

KEEP IMPLEMENTATION AS CLEAN AND CONDENSED AS YOU CAN
KEEP LINES OF CODE AS LOW AS POSSIBLE. 
FAVOR CLEAN ABSTRACTIONS WITH GOOD REUSE.
Do not write readme.md files until the end once the application reaches maturity.
Create a flake and use its devshell for any tooling you need.

# FOLLOW THIS ORDER
- read all docs and understand them.
2. create a full scaffold of the code base. Only Implementation, no tests.
  - THINK DEEP HERE. THIS NEEDS INTENSE FORWARD PLANNING
  - plan directory structure and files. Keep this focused, do not sprawl.
  - stub out functions and core interfaces. Leave focused comments, full implementation is not necessary.
  - create full data models, try and seperate these into seperate files for easy reference
3. core engine implementations, not full application logic
  - This should focus on mastering the core technologies and how they will be reused throughout nixmox
  - Make a strong foundation here, we want to use the best of these libraries and abstraction. This can heavily reduce the amount we must implement.
  - For each of them, do deep research on how they work capturing all of the details we will need. Use context7 and websearch
  - After research, begin implementation of the core engine. Think about how these functions will be reused throughout the application. 
  - I have put some examples of things you should cover when tackling each engine, it is not a complete list, use the specs to infer anything I missed.
  - You may create small go tests here after implementing the engine.
  - NATS and Jetstream Core Comms Engine
    - how do we stream logs or any host telemetry etc
    - how to use kv storage, replication
    - how to do events, retry/replay lifecycle
    - raft and how we will handle quorum.
    - how we do authorizatilon and authentication
  - OTEL Telemetry Engine
    - In this case otel is not only for app monitoring but also handles internal features
    - how will we instrument traces through nats and the browser interfaces
    - how logs will be attaced to traces
    - logs from both task execution and logs produced by nixmox
    - how metrics are created and used. Some metric might be internal to nixmox while some host telemetry is sent to otlp and nats.
    - how you the ai agent can use this to debug the application, maybe setup a diag/testing collector outside of nixmox?
  - Task Engine
    - task handling
    - log streaming
    - command execution
    - state machine and failure scenarios
    - log adapters for extracting progress and other information, look at nix output monitor, nix diff etc
  - User Interface Engine
    - charm, htmx and wasm
    - Think about all common functionality that the user interfaces will need.
    - Consolidate and implement this functionality
    - Plan ahead if new ways to interact with the application come up, how can they easily slot in so it is seamless across cli, tui and browser.
  - Nix Engine
    - handle nix related actions
    - formulate commands to send to task engine
    - interactions with nix store for cache or other related functions
  - Git Engine
    - handle git interactions for cloning, pulling, pushing, publishing etc
    - handle local git store logic

Acceptance validation should be automated as much as possible with virtual machines and integration tests.

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

## Git workflow

- The agent may commit and push changes on development branches.
- Create or switch to a development branch before committing.
- Never commit or push directly to `main`, merge into `main`, or force-push
  without explicit authorization.
- Do not include unrelated changes or credentials in commits.
