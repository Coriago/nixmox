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

## CLI reference

Prefix every command below with `nixmox`. Signatures include required arguments
and required named flags only; `<value>` is a placeholder. Optional arguments,
global flags, and inputs required only in particular flows are listed separately.
Names resolve to opaque IDs only when unambiguous. These tables index the proposed
interfaces in the feature docs; unresolved contracts are listed under
[Inconsistencies and gaps](#inconsistencies-and-gaps).

### Console, identity, and networking

Sources: [runtime](runtime-configuration.md), [identity](identity-and-trust.md),
[onboarding](host-onboarding.md), [diagnostics](technical-requirements.md).

| Command | Description |
| --- | --- |
| `serve` | Start the daemon and gateway. |
| `tui` | Open the terminal interface. |
| `settings show` | Show effective runtime settings and ownership. |
| `identity show` | Show this node's identity and membership. |
| `identity reset --confirm-host <host>` | Reset local identity after review; requires an unjoined node. |
| `network status` | Show local link/address readiness. |
| `network check` | Refresh local network readiness without an internet probe. |
| `network wifi scan` | Refresh available Wi-Fi networks. |
| `network wifi connect --network <network>` | Connect to a selected network; prompt securely for its password. |
| `discovery list` | List live node and cluster discovery results. |
| `discovery search` | Run a fresh 30-second discovery search. |
| `diagnostics collect --host <host>` | Collect a sanitized diagnostic bundle. |

### Clusters and hosts

Sources: [host lifecycle](host-inventory-and-lifecycle.md),
[identity](identity-and-trust.md), [onboarding](host-onboarding.md),
[tasks](task-engine.md), [deployment](deployment-and-rollback.md).

| Command | Description |
| --- | --- |
| `cluster create <name>` | Create a cluster with a provisional control plane. |
| `cluster join --address <address> --name <name>` | Request admission to the named cluster. |
| `cluster status` | Show membership, quorum, and cluster health. |
| `cluster requests list` | List cluster-side join requests. |
| `cluster requests approve <request> --version <version>` | Approve the exact request and stored node key. |
| `cluster add --discovery <discovery>` | Admit a discovered node after reviewing its identity. |
| `hosts list` | List hosts and their current state. |
| `hosts show <host>` | Show membership, configuration, reachability, and health. |
| `hosts endpoints <host>` | Show verified endpoints and peer connectivity. |
| `hosts assign <host> --repository <repository> --configuration <name>` | Bind a host to a named configuration. |
| `hosts bootstrap --ssh <user@address> --credential <credential>` | Start a temporary agent over verified SSH and enroll it. |
| `hosts refresh <host>` | Refresh inventory and health observations. |
| `hosts diagnostics <host>` | Collect host diagnostics. |
| `hosts reboot <host> --confirm-host <host>` | Reboot the host after identity confirmation. |
| `hosts reboot <host> --into installer --confirm-host <host>` | Enter a verified installer through normal reboot. |
| `hosts shutdown <host> --confirm-host <host>` | Schedule shutdown and report observed state. |
| `hosts leave <host>` | Leave gracefully and revoke cluster membership. |
| `hosts remove <host> --force --confirm-host <host>` | Commit forced removal using surviving quorum. |

### Repositories and configurations

Sources: [GitOps](gitops.md), [configuration generation](configuration-generation.md),
[runtime](runtime-configuration.md).

| Command | Description |
| --- | --- |
| `repositories import <name> --url <url> --upstream <branch>` | Register a remote-backed repository. |
| `repositories list` | List repositories, upstream freshness, and pending changes. |
| `repositories refs <repository>` | List upstream and local-change references. |
| `repositories fetch <repository>` | Fetch and verify the remote base. |
| `repositories remote set <repository> --url <url>` | Change an editable remote URL; upstream handling is unresolved. |
| `repositories credential set <repository> <credential>` | Select the repository's access credential. |
| `repositories rebase <repository> --ref <ref> --onto <ref>` | Rebase a change onto a pinned upstream revision. |
| `repositories rebase-resolve <task> --files <directory>` | Submit files resolving a retained rebase conflict. |
| `repositories rebase-abort <task>` | Abandon a conflicted rebase without changing the accepted head. |
| `repositories publish <repository> --ref <ref>` | Push the selected local change to a remote branch. |
| `repositories gc <repository> --dry-run` | Produce a reviewable artifact cleanup plan. |
| `repositories gc <repository> --plan <plan>` | Execute a reviewed cleanup plan after revalidation. |
| `artifacts show <artifact>` | Show exact Git source, retention, and linked evidence. |
| `artifacts delete <artifact>` | Request reviewed deletion with dependency checks. |
| `configurations list --repository <repository>` | List named configurations at an inspected revision. |
| `configurations templates <repository>` | List repository configuration templates. |
| `configurations generate <repository>` | Render a draft from a selected template and form values. |
| `configurations draft show <draft>` | Show draft files and validation state. |
| `configurations draft validate <draft>` | Validate without a full system build. |
| `configurations draft commit <draft> --expected-version <version>` | Commit the reviewed draft as replicated local changes. |

### Preparation and installer media

Sources: [onboarding](host-onboarding.md), [installer images](installer-images.md).

| Command | Description |
| --- | --- |
| `onboarding inspect <host>` | Collect authoritative hardware and disk observations. |
| `onboarding prepare <host> --configuration <configuration> --observation <observation>` | Render hardware/disk changes and optional SOPS updates for review. |
| `onboarding show <preparation>` | Show preparation readiness and selected devices. |
| `onboarding commit <preparation> --expected-version <version>` | Commit reviewed preparation and pin its source. |
| `onboarding sops credential set <credential>` | Select the SOPS update credential. |
| `media profiles` | List built-in and custom installer profiles. |
| `media profiles add --file <file>` | Register a Git-backed image recipe from JSON. |
| `media build` | Build a selected installer profile, preferring the writing node. |
| `media fetch --release <release>` | Fetch and verify a release image for a selected profile. |
| `media devices` | List devices attached to the writing node. |
| `media plan --image <image> --device <device>` | Bind an image to an observed, eligible destination device. |
| `media write --plan <plan> --confirm-device <device>` | Write the reviewed image and verify readback. |

### Deployment and tasks

Sources: [deployment](deployment-and-rollback.md), [task engine](task-engine.md).

| Command | Description |
| --- | --- |
| `apply` | Build, review, and apply the local node's latest assigned configuration. |
| `deploy apply` | Alias of `apply`, with identical defaults. |
| `deploy preview <host> --repository <repository> --configuration <name> --ref <ref>` | Evaluate and preview source changes without a full build. |
| `deploy build <host> --repository <repository> --configuration <name> --ref <ref> --builder <host>` | Build an explicit candidate without activation. |
| `deploy candidate show <candidate>` | Show pinned source, output, and comparison evidence. |
| `deploy diff <host> --candidate <candidate>` | Compare a built candidate with the target's observed system. |
| `deploy diff show <diff>` | Show the structured package diff and complete summary. |
| `deploy approve <task> --diff <diff> --diff-version <version>` | Approve the exact deployment review and resume its task. |
| `deploy generations <host>` | List recorded system generations and output availability. |
| `deploy rollback <host> --generation <generation> --mode <mode>` | Activate or boot-stage a retained generation. |
| `deploy install <host>` | Guide first/fresh installation through entry, preparation, and disk review. |
| `tasks list` | List tasks and their states. |
| `tasks show <task>` | Show task state, result, and eligible actions. |
| `tasks watch <task>` | Follow structured progress and state updates. |
| `tasks progress <task>` | Read the current progress snapshot. |
| `tasks logs <task>` | Read retained task logs. |
| `tasks cancel <task>` | Request cancellation at a safe boundary. |
| `tasks retry <task>` | Create a linked retry; destructive work requires renewed review. |

### Credentials and cache

Sources: [identity](identity-and-trust.md), [cache gateway](cache-gateway.md).

| Command | Description |
| --- | --- |
| `credentials import <name> --kind <kind> --node <host> --file <file>` | Import a local secret file into a selected node's protected store. |
| `credentials list` | List credential metadata and available replicas, never values. |
| `credentials check <credential> --repository <repository>` | Test credential access to a repository. |
| `cache credential set <credential>` | Select the cache signing credential. |
| `cache status` | Show local gateway and peer availability. |
| `cache locate <store-path>` | Look up verified holders of a Nix store output. |

### Global and shared flags

| Flag | Scope and behavior |
| --- | --- |
| `--endpoint <url>` | Select the console; precedence is flag, `NIXMOX_ENDPOINT`, local configuration, then `http://127.0.0.1:63170`. |
| `--json` | Emit API resource representations and never prompt. |
| `--wait` | Mutations: wait for completion or required approval; Apply waits by default. |
| `--command-id <id>` | Mutations: preserve retry identity across uncertain submissions. |

Mutations print command/task IDs. Without waiting, exit 0 means accepted. Waiting
exit codes: 0 succeeded, 1 failed/cancelled, 2 invalid input, 3 unavailable/unknown,
4 awaiting approval. A timeout requires receipt lookup before retrying the same ID.

### Optional arguments and command flags

| Commands | Optional inputs and behavior |
| --- | --- |
| `serve` | `--config <file>` selects runtime YAML. |
| `tui` | `--installer` starts in installer network/onboarding screens. |
| `repositories publish` | `--pull-request` requests a PR after pushing. |
| `configurations draft show`, `onboarding show` | `--diff` includes the proposed file changes. |
| `apply`, `deploy apply` | Positional `<host>` overrides the local target; required with a remote console or no local daemon. |
| `apply`, `deploy apply` | `--repository <repository>`, `--configuration <name>`, `--ref <ref>` override the binding; payload mapping needs clarification. |
| `apply`, `deploy apply` | `--candidate <candidate>` uses an exact built output; `--builder <host>` overrides automatic building; these are mutually exclusive. |
| `apply`, `deploy apply` | `--mode <switch\|boot>` defaults to `switch`; `--yes` auto-approves only the deployment diff; `--detach` returns on acceptance and conflicts with `--wait`. |
| `deploy install` | `--preparation <preparation>`, `--builder <host>`, `--installer <installer>`, `--method <auto\|reboot\|kexec>`; default method is `auto`. |
| `deploy install` | `--task <task>` resumes the existing installation with new review input. |
| `media build` | `--builder <host>` overrides writer-preferred builder selection. |
| `tasks list` | `--host <host>` filters by target. |
| `tasks logs` | `--follow` follows new log bytes. |

### Conditional inputs and confirmations

These can be omitted only when a documented default or interactive review supplies
them. They are never permission to skip validation or destructive confirmation.

| Commands | Input required by the selected flow |
| --- | --- |
| `cluster add` | Scripts supply `--key-fingerprint <fingerprint>`; interactive clients review the discovered key. |
| `hosts bootstrap` | Scripts supply `--host-key-fingerprint <fingerprint>`; interactive clients verify the SSH host key. |
| `configurations generate` | `--template <template>` selects among multiple templates; repeat `--set <field=value>` for required values without defaults or prompted answers. |
| `onboarding prepare` | `--hardware-template <template>`, `--disk-template <template>` select among multiple compatible templates; repeat `--set <stage.field=value>` for required form fields. Disk selections are always explicit. |
| `media build`, `media fetch` | `--profile <profile>` is required for noninteractive selection when multiple profiles exist; a sole profile is selected automatically. |
| `media build`, `media devices`, `media plan` | `--host <host>` selects the writer; it may be omitted with that node's local daemon, but remote-console use requires it. |
| `deploy install` | `--confirm-host <host>` approves installer entry; repeat `--confirm-device <path>` for every erase target at destructive review. See the confirmation inconsistency below. |

## HTTP endpoint reference

All management routes below are prefixed with `/api/v1`. Required path/query
parameters are shown; optional query controls are separate. Resource IDs are
opaque. Feature docs sometimes omit the prefix; this index normalizes it.

### Commands, secrets, and events

| Endpoint | Description |
| --- | --- |
| `POST /api/v1/commands` | Submit a typed mutation envelope; return a command/task receipt (202). |
| `GET /api/v1/commands/{command_id}` | Recover admission/result status after an uncertain submission. |
| `POST /api/v1/credentials/import` | Stage credential metadata, storage node, and secret bytes through protected ingress; dispatch a secret-free command. |
| `POST /api/v1/network/wifi/credentials` | Stage a local Wi-Fi password and return a short-lived reference. |
| `GET /api/v1/events` | Open SSE with a snapshot, versioned updates, task progress, and log events. |
| `GET /api/v1/membership-status?node_id=<node_id>&membership_epoch=<epoch>` | Read authenticated membership/removal status, including for revoked nodes. |

Mutation bodies follow the [command contract](api-and-interfaces.md#command-api)
and feature operation tables; they are not separate resource POST/DELETE routes.
Ordinary envelopes reject raw secrets. Secret ingress requires local/protected
transport; membership-status requires the previously pinned server trust.

### Collections and details

Each row lists two GET endpoints: collection and detail, following the
[common detail-route convention](api-and-interfaces.md#reads-and-events).
Collections return `{items, next_cursor}`. Detail schemas not supplied in feature
docs remain a specification gap, not an invented object contract.

| Collection endpoint | Detail endpoint | Description |
| --- | --- | --- |
| `GET /api/v1/hosts` | `GET /api/v1/hosts/{id}` | Host inventory, bindings, reachability, and health. |
| `GET /api/v1/clusters` | `GET /api/v1/clusters/{id}` | Cluster resources. |
| `GET /api/v1/discoveries` | `GET /api/v1/discoveries/{id}` | Expiring discovery hints for nodes/clusters. |
| `GET /api/v1/repositories` | `GET /api/v1/repositories/{id}` | Remote-backed repository metadata. |
| `GET /api/v1/configurations` | `GET /api/v1/configurations/{id}` | Named configuration descriptors and readiness. |
| `GET /api/v1/drafts` | `GET /api/v1/drafts/{id}` | Owner-local drafts and validation state. |
| `GET /api/v1/artifacts` | `GET /api/v1/artifacts/{id}` | Retained Git changes, pins, and evidence. |
| `GET /api/v1/preparations` | `GET /api/v1/preparations/{id}` | Hardware/disk preparation and confirmation state. |
| `GET /api/v1/media` | `GET /api/v1/media/{id}` | Installer image metadata and provenance. |
| `GET /api/v1/candidates` | `GET /api/v1/candidates/{id}` | Pinned system build outputs. |
| `GET /api/v1/deployments` | `GET /api/v1/deployments/{id}` | Activation, generation, and observation evidence. |
| `GET /api/v1/diffs` | `GET /api/v1/diffs/{id}` | Structured package comparisons; detail includes summary and first page. |
| `GET /api/v1/tasks` | `GET /api/v1/tasks/{id}` | Task state, progress summary, results, and errors. |
| `GET /api/v1/credentials` | `GET /api/v1/credentials/{id}` | Credential metadata and availability, never secret values. |
| `GET /api/v1/observations` | `GET /api/v1/observations/{id}` | Service-collected hardware and device facts. |
| `GET /api/v1/publications` | `GET /api/v1/publications/{id}` | Remote branch/PR publication evidence. |
| `GET /api/v1/cluster/requests` | `GET /api/v1/cluster/requests/{id}` | Join approval queue and request identity/version. |

### Singleton and nested reads

| Endpoint | Description |
| --- | --- |
| `GET /api/v1/settings` | Effective runtime settings and declaration ownership. |
| `GET /api/v1/identity` | Local node identity and membership epoch. |
| `GET /api/v1/cluster/status` | Current cluster membership, quorum, and health. |
| `GET /api/v1/cache/status` | Local gateway status and peer availability. |
| `GET /api/v1/cache/locations?path=<store-path>` | Bounded live lookup of output holders; URL-encode the path. |
| `GET /api/v1/network/status` | Local installer link/address readiness. |
| `GET /api/v1/network/wifi/networks` | Observed Wi-Fi networks and security/signal metadata. |
| `GET /api/v1/repositories/{id}/refs` | Upstream and accepted local-change references. |
| `GET /api/v1/repositories/{id}/diff?from=<revision>&to=<revision>` | Source diff between selected revisions. |
| `GET /api/v1/repositories/{id}/templates` | Repository-defined form/template catalog. |
| `GET /api/v1/drafts/{id}/diff` | Proposed draft file changes. |
| `GET /api/v1/preparations/{id}/diff` | Combined hardware, disk, and optional SOPS changes. |
| `GET /api/v1/media/profiles` | Built-in and registered installer profiles. |
| `GET /api/v1/hosts/{id}/disks` | Observed host disks and eligibility. |
| `GET /api/v1/hosts/{id}/generations` | Retained system generations and output availability. |
| `GET /api/v1/hosts/{id}/installer` | Installer-entry capabilities, recovery image, and protected devices. |
| `GET /api/v1/diffs/{id}/changes` | Further pages of structured package changes. |
| `GET /api/v1/tasks/{id}/logs` | Retained log bytes with absolute and next offsets. |
| `GET /api/v1/tasks/{id}/progress` | Versioned progress snapshot and bounded active preview. |
| `GET /api/v1/tasks/{id}/activities` | Paginated build/download/transfer activities. |
| `GET /api/v1/tasks/{id}/activities/{activity_id}` | One activity's state, attempt, counters, and timing. |

### Query controls

| Reads | Controls described in the specs |
| --- | --- |
| Collections and activity lists | Pagination is required; a common request parameter/default/limit contract is not yet defined. |
| Events | `topics=hosts,tasks,repositories` is the documented selector example; omission behavior and the complete topic list are unspecified. |
| Diff changes | `kind=<category>`, `cursor=<cursor>` filter and paginate rows. Search is required, but its parameter is unspecified. |
| Task logs | `offset=<byte-offset>`, `limit=<bytes>` select a chunk; the example uses 0 and 65536, without defining omission defaults. |
| Task list | Host, kind, state, and time filters are required; exact HTTP parameter names are unspecified. |

### Nix cache transport

These routes share the API listener but have **no `/api/v1` prefix**.
See [cache gateway](cache-gateway.md).

| Endpoint or namespace | Description |
| --- | --- |
| `/cache/` | Local-only Nix substituter namespace; look up peers and stream exact outputs. Child URL syntax is not fully specified. |
| `GET /store-cache/nix-cache-info` | Serve cache protocol metadata. |
| `GET /store-cache/{store-hash}.narinfo` | Serve valid local output metadata, signatures, and archive URL. |
| `GET /store-cache/nar/{store-hash}/{nar-hash}.nar` | Stream the exact local output after validating both hashes. |
| `HEAD /store-cache/nix-cache-info` | Return cache metadata headers without a body. |
| `HEAD /store-cache/{store-hash}.narinfo` | Return output metadata headers without a body. |
| `HEAD /store-cache/nar/{store-hash}/{nar-hash}.nar` | Return archive headers without exporting a body. |

Peer store routes require machine authorization; uploads are unsupported. The
gateway rewrites archive URLs without changing output integrity metadata.

## Inconsistencies and gaps

These are unresolved findings from the rewrite docs, not new CLI/API decisions.

| Area | Finding / required clarification |
| --- | --- |
| Installation confirmation | [Deployment](deployment-and-rollback.md#reboot-into-installer-and-fresh-install) allows `deploy install <host>` to pause for later disk review; [onboarding acceptance](host-onboarding.md#api-and-acceptance) says omitting any `--confirm-device` is an error. Specify submission versus erase/resume requirements. |
| Missing CLI parity | Document syntax for `cluster.request.reject`, `host.adopt`, `configuration.draft.edit`, `publication.merge`, and `credential.remove`, all defined as operations in their feature docs. Do not infer public verbs from type names. |
| Incomplete operation registry | [Identity reset](identity-and-trust.md#identity-record-and-lifecycle) and [diagnostics collect](technical-requirements.md#opentelemetry-and-diagnostics) lack explicit command-type/payload mappings. Clarify whether diagnostics aliases `host.diagnostics`; several other operation payloads are prose-only. |
| Missing/defaulted CLI inputs | [Host assignment](host-inventory-and-lifecycle.md#discovery-and-enrollment) requires `ref_id` in its payload but omits its CLI selection/default. [Remote set](gitops.md#operation-and-read-api) requires `upstream_branch`, absent from its CLI example. Generation needs `base_commit` without a CLI resolution rule. |
| Deployment selectors/defaults | [Apply defaults](deployment-and-rollback.md#apply-latest) allow repository/configuration/ref overrides, but `deployment.apply` lists no source selector. Preview/build have explicit source payloads, and build requires `builder_id`; clarify whether Apply's defaults extend to them. Rollback requires `mode` without an explicit default. The index retains these explicit inputs. |
| Other argument contracts | Specify whether configuration listing can omit its repository, how non-Git credentials are checked, and whether media fetch defaults its release. The index retains the only documented explicit forms. |
| Review/version inputs | Define transport for artifact deletion review, GC-plan creation, rebase draft versions, and installation task-version/review binding. Draft/preparation commits require repository versions beyond their shown `--expected-version`; document how clients obtain and submit both. |
| Missing read/download contracts | Define media-plan retrieval, diagnostic bundle and image downloads, retained rebase draft/file access, and whether `hosts endpoints` reads host detail or a dedicated route. No paths are specified for these flows. |
| Read schemas and selectors | [Common reads](api-and-interfaces.md#reads-and-events) define collection/detail conventions but leave several schemas, filters, cursor bounds, event topics, and log defaults unspecified. Define revision selection for configuration/template catalogs and full-log/per-build-log retrieval. |
| Bootstrap admission | [Technical requirements](technical-requirements.md) say all durable mutations use JetStream and quorum loss blocks new commands; [onboarding](host-onboarding.md#installer-startup-and-network-setup) exempts local bootstrap operations. Specify their admission, persistence, and receipt behavior before enrollment. |
| Cache and enrollment transport | [Cache](cache-gateway.md#routes-and-transport) leaves aggregate `/cache/` child routes implicit; [identity](identity-and-trust.md#enrollment-and-trust-boundary) describes TLS enrollment without concrete HTTP routes. Define these separately from ordinary command submission. |
| Endpoint records | [Host inventory](host-inventory-and-lifecycle.md#host-record) uses port 63173 and [identity handoff](identity-and-trust.md#address-handoff-example) uses 63172 in untyped `endpoints`; [runtime](runtime-configuration.md#service-ports) assigns those to leaf and route listeners. Add protocol/service fields to distinguish them from API/cache port 63170. |

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
