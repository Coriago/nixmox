# Nixmox implementation specification

Nixmox manages NixOS configurations, installation, deployment, and cluster lifecycle
from any node. Browser, TUI, and CLI expose the same operations. Git publication
of local changes is optional. Every repository has a remote base that nodes pull
independently; JetStream retains unpublished changes, not full repositories.

## How to implement this specification

These are proposed interfaces for the rewrite, not commands or options claimed to
exist. Product requirements established here are mandatory. Concrete defaults,
names, schemas, and algorithms are the proposed implementation contract. Items
explicitly marked **Decision required** need agreement before dependent work.
Do not replace an undecided mechanism with an undocumented assumption.

Read the API contract and runtime configuration first. Feature documents define
objects, transitions, operations, user flows, and acceptance cases. Examples use
JSON stubs: fields shown are required unless described as optional or nullable.
Additional persisted fields require a schema-version decision.

| Document | Implementation responsibility |
| --- | --- |
| [API and interfaces](api-and-interfaces.md) | IDs, commands, errors, events, CLI/TUI/browser conventions |
| [Runtime configuration](runtime-configuration.md) | NixOS module, daemon options, credentials, precedence |
| [Configuration generation](configuration-generation.md) | Form schema, templates, drafts, validation |
| [GitOps](gitops.md) | Repositories, branches, artifacts, replication, publication |
| [Cache gateway](cache-gateway.md) | Peer output lookup, streaming pulls, store serving, failure handling |
| [Host lifecycle](host-inventory-and-lifecycle.md) | Discovery, joining, pending/full membership, removal |
| [Identity and trust](identity-and-trust.md) | Join approval, managed TLS/NATS permissions, leaf streaming, canonical secret/state inventory and preservation |
| [Onboarding](host-onboarding.md) | Agent bootstrap, observations, preparation, disk confirmation, installation backend |
| [Installer images](installer-images.md) | Architecture/board profiles, custom recipes, builders, USB/SD images |
| [Deployment and rollback](deployment-and-rollback.md) | Apply versus Install, installer entry, source pinning, activation, generations |
| [Task engine](task-engine.md) | Durable execution, cancellation, logs, recovery |
| [Technical requirements](technical-requirements.md) | NATS, shared UI logic, OTel, persistence, verification |

## Worked environment

All documents use these fixture aliases. Production IDs are generated opaque IDs;
labels resolve to them only when unambiguous. Commit hashes in examples are fixtures.

| Alias | Object |
| --- | --- |
| homelab / c_lab | Cluster |
| alder / n_alder | Full member, console and builder, x86_64-linux |
| birch / n_birch | Full member and alternative builder, x86_64-linux |
| cedar / n_cedar | Live installer, then installed full member |
| lab / r_lab | Remote-backed configuration repository, upstream main |
| services / r_services | Independent second repository |
| cedar / cfg_cedar | Named configuration in lab |

```text
Form → draft → validated local commit → replicated Git artifact
                        ↓
Pending host → preparation → committed hardware changes → install → full member
                                                           ↓
                                                deployment evidence
                                                           ↓
                                                optional push and PR
```

A branch represents pending changes, not a host. A host whose configuration is
already upstream tracks that branch and its named configuration directly.

Apply updates an installed NixOS system without formatting, using standard Nix
build/rebuild tools. `nixmox deploy install <host>` is the single first/fresh-install
workflow, with partition/install execution in the installer environment. Onboarding
commands only inspect/prepare/commit.
Builders, deployment targets, and image writers pull Nix outputs through a local
streaming cache gateway. Peer serving shares the API port; NATS carries control
and progress, not artifact bytes.
USB/SD, kexec, an installed agent, and temporary SSH agent bootstrap converge on
the same agent/task protocol. Without kexec, a compatible boot adapter can use
existing installer media or prepare a spare device not required by the running
system. Only hosts without either entry route are installation-incompatible;
a verified installer can install directly.

## Implementation order and gates

1. Domain objects, versioned persistence, command receipts, task engine.
2. Identity, discovery, cluster lifecycle, reconnecting read/event API.
3. Remote base fetching, local configuration changes, JetStream artifact storage.
4. Candidate builds, deployment/rollback, target preparation, USB installation.
5. Optional publication/credentials and selected secret updater.

Build CLI, browser, and TUI adapters alongside each feature; interface parity is
an acceptance requirement, not a final polish step. The foundational gates are
restart-safe command admission, exact revision execution, and no destructive replay.

Generic UEFI uses the `efibootmgr` BootNext adapter. SOPS preparation
uses a bundled anchor updater with configured rule selectors and a custom override.
Kexec uses `kexec-tools`; Pi
reboot adapters cover SD U-Boot and EEPROM boot-order selection.
Installer setup uses link/address readiness without internet probing. Manual force removal requires
quorum; returning nodes become unjoined only after authenticating cluster removal.
V1 includes host/cluster health, but no rollouts,
identity recovery/backup, or version-compatibility management.
Each owner document gives a concrete proposal or bounded options. Other numerical
defaults are tunable implementation proposals, not unresolved product decisions.
