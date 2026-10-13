# GitHub deployment fixture

This is a standalone deployment flake for disposable nixmox VMs. It lives outside
`nix/`, tracks `https://github.com/Coriago/nixmox.git` on `main`, and exports
`nixosConfigurations.node1`, `node2`, and `node3`.

`flake.lock` pins nixpkgs to the same revision as the repository's root flake.
The relative `nixmox` input uses the parent repository at the **same pulled Git
commit**, including its module and Go source. It does not fetch a second nixmox
revision. Keep this folder inside the repository; copying only `e2e/` loses that
parent input. Use a recent Nix with relative flake input support (the VM's pinned
Nix supports this).

The shared VM module preserves the demo hostname, cluster interface, filesystem
and console when switching. The deployed configuration additionally installs:

- `/etc/nixmox-e2e-generation`, containing `github-main-e2e-v1`;
- GNU `hello` in the system environment;
- `nixmox-e2e-proof.service`, which copies the marker and writes `hello` output to
  `/var/lib/nixmox-e2e/` during activation.

These are absent from the original `nix/` demo configuration. Checking all three,
along with the active system profile, distinguishes successful activation from
merely fetching Git, building a closure, or creating a boot generation.

## Commit, push, then run

Commit the fixture **and the accompanying module/application changes** to `main`
and push. In particular, the agent needs `flake_directory`/`flake_attribute`
selection and the queued task changes; pushing only this folder is insufficient.
Then run from the repository root:

```sh
nix run .#github-e2e
```

This boots a fresh local `node1` demo VM and uses its API to:

1. Verify the bootstrap has neither the marker nor `hello`.
2. Pull the real GitHub repository's `main` branch and record its exact SHA.
3. Submit Apply with mode `switch`, selecting `e2e#node1` at that SHA.
4. Let the **guest** evaluate and build the source configuration and nixmox
   package, using its ordinary Internet access for inputs/substitutes.
5. Verify the changed active system, marker, package, proof unit, running nixmox,
   apply history, and command logs.

The runner requires KVM, Internet access and free local ports 8081/2221. It shuts
down its VM on completion and prints a unique `/tmp/nixmox-github-e2e.*` directory
containing its disk, serial console, tested SHA, job responses/logs and history.
The VM has 4 GiB RAM and a sparse 16 GiB disk; its read-only seed store contains
only the bootstrap closure, not the host’s whole Nix store.
Builds can take up to an hour; errors fail the test and retain the artifacts.
It is an opt-in `nix run` app because ordinary Nix build checks have no network.
The existing offline checks remain useful lower-level regression tests.

The target generation is **not** supplied as a prebuilt closure. Normal binary
cache substitution is permitted, as on a real deployment. The agent isn't
restarted mid-apply: the module deliberately keeps the running daemon alive
across its own rebuild. Testing a restart into the newly installed agent, reboot,
rollback, and multi-host custom tasks can be added as subsequent scenarios.

Do not run this VM configuration on a physical machine. It inherits the demo's
root password and is intended only for a disposable local VM.

## Manual cluster scenario

```sh
nix run .#demo
```

The original demo nodes now start configured with the GitHub URL, branch `main`,
flake directory `e2e`, and their respective `node1`/`node2`/`node3` attributes.
Open a node's GitOps page, pull, wait for the pull task, then Apply. Automatic
apply and background polling are disabled so the chosen revision is explicit.
The demo's existing cluster creation/join workflow remains available for later
remote task scenarios.

For direct inspection after committing the files:

```sh
nix eval ./e2e#nixosConfigurations.node1.config.services.nixmox.gitops --json
nix build ./e2e#nixosConfigurations.node1.config.system.build.toplevel
```

For untracked files, evaluate from a temporary Git snapshot of the **whole**
repository. `path:./e2e` alone cannot resolve the parent input outside its copied
source tree. Do not update the lock file just to work around missing tracked files.
