# Nixmox rewrite specifications

Nixmox manages NixOS configurations, installation, deployment, and cluster lifecycle
from any node through CLI, browser, and TUI. Repositories have remote bases;
JetStream retains unpublished changes. Publishing those changes is optional.

These documents define proposed rewrite interfaces, not currently available commands.
Defaults, names, and schemas form the implementation contract. Items marked
**Decision required** or **Implementation gate** must be resolved before dependent work.
JSON examples are partial; shown fields are required unless marked optional or nullable.
Persisted records follow their schema version.

Read the API and runtime configuration first. Feature pages own their CLI examples,
operation payloads, read routes, and behavior; shared rules belong to the API contract.

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

## V1 scope

Apply updates installed NixOS without formatting. Install handles first/fresh
installation through a verified installer; onboarding only inspects and prepares.
Multi-host rollouts, identity recovery/backup, and version-compatibility management
are outside v1. Verification requirements are in [technical requirements](technical-requirements.md#acceptance-matrix).
