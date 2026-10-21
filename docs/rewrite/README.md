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
| [Host lifecycle](host-inventory-and-lifecycle.md) | Discovery, joining, pending/full membership, removal |
| [Identity and trust](identity-and-trust.md) | Stable identities, interface handoff, credentials |
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
workflow, using nixos-anywhere/Disko. Onboarding commands only inspect/prepare/commit.
USB/SD, kexec, an installed agent, and temporary SSH agent bootstrap converge on
the same agent/task protocol. A running OS without supported kexec or reboot entry
is installation-incompatible; a verified installer can install directly.

## Implementation order and gates

1. Domain objects, versioned persistence, command receipts, task engine.
2. Identity, discovery, cluster lifecycle, reconnecting read/event API.
3. Remote base fetching, local configuration changes, JetStream artifact storage.
4. Candidate builds, deployment/rollback, target preparation, USB installation.
5. Optional publication/credentials and selected secret updater.

Build CLI, browser, and TUI adapters alongside each feature; interface parity is
an acceptance requirement, not a final polish step. The foundational gates are
restart-safe command admission, exact revision execution, and no destructive replay.

Decisions blocking complete implementation: production machine-auth issuance and
recovery; optional SOPS updater; tested bootstrap/bootloader platform support;
cross-node Nix output trust/transfer; mixed-version upgrade policy.
Each owner document gives a concrete proposal or bounded options. Other numerical
defaults are tunable implementation proposals, not unresolved product decisions.
