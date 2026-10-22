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
Feature documents define required fields; persisted changes follow their schema version.

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
interfaces in the feature docs; shared rules and remaining validation gates are
linked under [Interface decisions](#interface-decisions).

### Console, identity, and networking

Sources: [runtime](runtime-configuration.md), [identity](identity-and-trust.md),
[onboarding](host-onboarding.md), [diagnostics](technical-requirements.md).

| Command | Description |
| --- | --- |
| `serve` | Start the daemon and gateway. |
| `tui` | Open the terminal interface. |
| `settings show` | Show effective runtime settings and ownership. |
| `identity show` | Show this node's identity and membership. |
| `identity reset --confirm-host <host>` | Reset the addressed daemon's own identity while unjoined. |
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
| `cluster requests reject <request> --version <version>` | Reject the exact pending join request. |
| `cluster add --discovery <discovery>` | Admit a discovered node after reviewing its identity. |
| `hosts list` | List hosts and their current state. |
| `hosts show <host>` | Show membership, configuration, reachability, and health. |
| `hosts endpoints <host>` | Show verified endpoints and peer connectivity. |
| `hosts assign <host> --repository <repository> --configuration <name>` | Bind a host to a named configuration. |
| `hosts adopt <host> --candidate <candidate> --confirm-host <host>` | Verify the running system against a pinned candidate and grant full membership. |
| `hosts bootstrap --ssh <user@address> --credential <credential>` | Start a temporary agent over verified SSH and enroll it. |
| `hosts refresh <host>` | Refresh inventory and health observations. |
| `hosts diagnostics <host>` | Collect host diagnostics. |
| `hosts reboot <host> --confirm-host <host>` | Reboot the host after identity confirmation. |
| `hosts reboot <host> --into installer` | Prepare and review entry into a verified installer through normal reboot. |
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
| `repositories remote set <repository> --url <url>` | Change an editable remote URL, preserving its upstream branch. |
| `repositories credential set <repository> <credential>` | Select the repository's access credential. |
| `repositories rebase <repository> --ref <ref> --onto <ref>` | Rebase a change onto a pinned upstream revision. |
| `repositories rebase-resolve <task> --files <directory> --expected-version <version>` | Submit files resolving the reviewed draft version. |
| `repositories rebase-abort <task>` | Abandon a conflicted rebase without changing the accepted head. |
| `repositories publish <repository> --ref <ref>` | Push the selected local change to a remote branch. |
| `publications merge <publication> --expected-head <commit>` | Merge the reviewed remote head after integration-base checks. |
| `repositories gc <repository> --dry-run` | Produce a reviewable artifact cleanup plan. |
| `repositories gc <repository> --plan <plan>` | Execute a reviewed cleanup plan after revalidation. |
| `artifacts show <artifact>` | Show exact Git source, retention, and linked evidence. |
| `artifacts delete <artifact>` | Plan and review deletion; interactive waiting guides confirmation. |
| `configurations list` | List named configurations across repositories with inspected revisions. |
| `configurations templates <repository>` | List repository configuration templates. |
| `configurations generate <repository>` | Render a draft from a selected template and form values. |
| `configurations draft show <draft>` | Show draft files and validation state. |
| `configurations draft edit <draft> --set <field=value> --expected-version <version>` | Patch typed form values and invalidate old validation. |
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
| `media fetch` | Fetch and verify the latest release image for a selected profile. |
| `media devices` | List devices attached to the writing node. |
| `media plan --image <image> --device <device>` | Bind an image to an observed, eligible destination device. |
| `media write --plan <plan> --confirm-device <device>` | Write the reviewed image and verify readback. |

### Deployment and tasks

Sources: [deployment](deployment-and-rollback.md), [task engine](task-engine.md).

| Command | Description |
| --- | --- |
| `apply` | Build, review, and apply the local node's latest assigned configuration. |
| `deploy apply` | Alias of `apply`, with identical defaults. |
| `deploy preview <host>` | Evaluate and preview the bound source without a full build. |
| `deploy build <host>` | Build a candidate using the bound source and automatic builder selection. |
| `deploy candidate show <candidate>` | Show pinned source, output, and comparison evidence. |
| `deploy diff <host> --candidate <candidate>` | Compare a built candidate with the target's observed system. |
| `deploy diff show <diff>` | Show the structured package diff and complete summary. |
| `deploy approve <task> --diff <diff> --diff-version <version>` | Approve the exact deployment review and resume its task. |
| `deploy generations <host>` | List recorded system generations and output availability. |
| `deploy rollback <host> --generation <generation>` | Switch to a retained generation after review. |
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
| `credentials check <credential>` | Run kind-specific access or local key validation. |
| `credentials remove <credential>` | Remove an unreferenced, app-managed credential and its replicas. |
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
| `hosts assign` | `--ref <ref>` overrides the selected configuration's tracked ref. |
| `repositories remote set` | `--upstream <branch>` explicitly changes the preserved upstream branch. |
| `repositories publish` | `--pull-request` requests a PR after pushing. |
| `artifacts delete` | `--dry-run` produces a plan only; `--plan <plan>` approves a reviewed plan; `--task <task>` resumes a paused deletion. |
| `configurations list` | `--repository <repository>` filters results; `--ref <ref>` or `--commit <commit>` requires that filter. |
| `configurations templates`, `configurations generate` | `--ref <ref>` or `--commit <commit>` overrides the accepted upstream revision; mutually exclusive. |
| `configurations draft show`, `onboarding show` | `--diff` includes the proposed file changes. |
| `apply`, `deploy apply` | Positional `<host>` overrides the local target; required with a remote console or no local daemon. |
| `apply`, `deploy apply`, `deploy preview`, `deploy build` | `--repository <repository>`, `--configuration <name>`, `--ref <ref>` or `--commit <commit>` populate the shared source selector; ref and commit are mutually exclusive. |
| `apply`, `deploy apply` | `--candidate <candidate>` uses an exact built output; `--builder <host>` overrides automatic building; these are mutually exclusive. |
| `apply`, `deploy apply` | `--mode <switch\|boot>` defaults to `switch`; `--yes` auto-approves only the deployment diff; `--detach` returns on acceptance and conflicts with `--wait`. |
| `deploy build` | `--builder <host>` overrides automatic builder selection. |
| `deploy rollback` | `--mode <switch\|boot>` defaults to `switch`. |
| `deploy install` | `--configuration <configuration>`, `--preparation <preparation>`, `--builder <host>`, `--installer <installer>`, `--method <auto\|reboot\|kexec>`; default method is `auto`. |
| `deploy install` | `--task <task>` resumes the existing installation with new review input. |
| `hosts reboot --into installer` | `--installer <installer>` selects recovery media; `--task <task>` resumes entry preparation/review. |
| `media build` | `--builder <host>` overrides writer-preferred builder selection. |
| `media fetch` | `--release <release>` overrides `latest`; the resolved manifest is pinned. |
| `tasks list` | `--host <host>`, `--kind <kind>`, `--state <state>`, `--since <timestamp>`, `--until <timestamp>` filter tasks. |
| `tasks logs` | `--follow`, `--activity <activity>`, `--offset <bytes>`, `--limit <bytes>` select/follow retained log bytes. |
| `hosts diagnostics`, `diagnostics collect` | `--since <timestamp>`, `--until <timestamp>` select a range; default last hour, maximum 24 hours. |

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
| `credentials check` | `--repository <repository>` is required for Git kinds and rejected for non-Git kinds. |
| `deploy install` | Scripted approval requires `--task <task> --review <review>` plus `--confirm-host <host>` for entry or repeated `--confirm-device <path>` for every erase target. Interactive waiting submits the displayed review. |
| `hosts reboot --into installer` | Scripted entry approval requires `--task <task> --review <review> --confirm-host <host>`; interactive waiting prompts at the bound review. |

## HTTP endpoint reference

All management routes below are prefixed with `/api/v1`. Required path/query
parameters are shown; optional query controls are separate. Resource IDs are
opaque. Feature docs sometimes omit the prefix; this index normalizes it.
CLI, TUI, and browser use HTTP on `http.port` (default 63170), including secret
submission. Backend-only routes use automated TLS on `machine.port` (default
63175); users do not configure certificates. See [service ports](runtime-configuration.md#service-ports).

### Commands, staging, and events

| Endpoint | Description |
| --- | --- |
| `POST /api/v1/commands` | Submit a typed mutation envelope; return a command/task receipt (202). |
| `GET /api/v1/commands/{command_id}` | Recover admission/result status after an uncertain submission. |
| `POST /api/v1/credentials/import` | Stage credential metadata, storage node, and secret bytes through redacted HTTP ingress; dispatch a secret-free command. |
| `POST /api/v1/network/wifi/credentials` | Stage a local Wi-Fi password and return a short-lived reference. |
| `POST /api/v1/drafts/{id}/resolutions?version=<version>` | Stage reviewed rebase files and return a version-bound reference. |
| `GET /api/v1/events` | Open SSE with a snapshot, versioned updates, task progress, and log events. |

Mutation bodies follow the [command contract](api-and-interfaces.md#command-api)
and feature operation tables; they are not separate resource POST/DELETE routes.
Ordinary envelopes reject raw secrets. Secret submission uses the HTTP console;
values stay out of logs, receipts, and task payloads.

### Backend enrollment, status, and credential forwarding

These backend calls use the machine TLS listener. Enrollment and membership status
are machine-only; credential import also has the HTTP console entry above. No
browser/CLI/TUI connects to the machine listener.

| Endpoint | Description |
| --- | --- |
| `POST /api/v1/enrollment/requests` | Submit a deduplicated enrollment request with node identity and CSRs. |
| `GET /api/v1/enrollment/requests/{id}` | Read the authenticated request owner's enrollment status. |
| `POST /api/v1/enrollment/requests/{id}/certificates` | Retrieve certificates for the approved node keys and membership. |
| `GET /api/v1/membership-status?node_id=<node_id>&membership_epoch=<epoch>` | Read membership/removal status using pinned server trust, including for revoked nodes. |
| `POST /api/v1/credentials/import` | Accept a credential forwarded by an authorized full-member daemon for storage on this node; apply the same staging/receipt contract. |

Enrollment authentication details are subject to the [TLS prototype gate](identity-and-trust.md#enrollment-http-exchange).

### Collections and details

Each row lists two GET endpoints: collection and detail, following the
[common detail-route convention](api-and-interfaces.md#reads-and-events).
Collections return `{items, next_cursor}`; feature docs define their object schemas.

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
| `GET /api/v1/gc-plans` | `GET /api/v1/gc-plans/{id}` | Reviewed artifact deletion sets and protected dependencies. |
| `GET /api/v1/media/plans` | `GET /api/v1/media/plans/{id}` | Image/device bindings and media-write reviews. |
| `GET /api/v1/diagnostics` | `GET /api/v1/diagnostics/{id}` | Sanitized diagnostic bundle metadata, retention, and download link. |

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
| `GET /api/v1/drafts/{id}/files` | Versioned draft/rebase file manifest. |
| `GET /api/v1/drafts/{id}/files/content?path=<relative-path>` | Stream one safe relative draft path. |
| `GET /api/v1/preparations/{id}/diff` | Combined hardware, disk, and optional SOPS changes. |
| `GET /api/v1/media/profiles` | Built-in and registered installer profiles. |
| `GET /api/v1/media/{id}/download` | Stream a verified installer image with digest and length. |
| `GET /api/v1/diagnostics/{id}/download` | Stream a retained sanitized diagnostic bundle. |
| `GET /api/v1/hosts/{id}/disks` | Observed host disks and eligibility. |
| `GET /api/v1/hosts/{id}/generations` | Retained system generations and output availability. |
| `GET /api/v1/hosts/{id}/installer` | Installer-entry capabilities, recovery image, and protected devices. |
| `GET /api/v1/diffs/{id}/changes` | Further pages of structured package changes. |
| `GET /api/v1/tasks/{id}/logs` | Retained log bytes with absolute and next offsets. |
| `GET /api/v1/tasks/{id}/logs/download` | Download a fixed snapshot of the retained task log. |
| `GET /api/v1/tasks/{id}/progress` | Versioned progress snapshot and bounded active preview. |
| `GET /api/v1/tasks/{id}/activities` | Paginated build/download/transfer activities. |
| `GET /api/v1/tasks/{id}/activities/{activity_id}` | One activity's state, attempt, counters, and timing. |
| `GET /api/v1/tasks/{id}/activities/{activity_id}/logs` | Read one build activity's retained log chunks. |
| `GET /api/v1/tasks/{id}/activities/{activity_id}/logs/download` | Download a fixed snapshot of one build log. |

### Query controls

| Reads | Controls described in the specs |
| --- | --- |
| Collections and activity lists | `cursor`, `limit`; ordering, consistency, and filters follow the [read contract](api-and-interfaces.md#reads-and-events). |
| Events | `topics`; `task_id` is required when subscribing to `task.logs`; optional `activity_id` selects a build log. |
| Configuration catalog | `repository_id`; mutually exclusive `ref_id` or `commit` requires it. |
| Template catalog | Mutually exclusive `ref_id` or `commit`. |
| Draft files/content | `version` pins file reads. |
| Diff changes | `kind`, `q`, `cursor`, `limit`. |
| Task/activity log chunks | `offset`, byte `limit`; bounds and retention follow the [log contract](task-engine.md#logs-and-interaction). |
| Task list | `host_id`, `kind`, `state`, `since`, `until`. |
| Activity list | `kind`, `state`, plus common pagination. |

### Nix cache transport

These routes have **no `/api/v1` prefix**. `/cache/` is local-only HTTP on
`http.port`; `/store-cache/` uses backend mTLS on `machine.port`.
See [cache gateway](cache-gateway.md).

| Endpoint | Description |
| --- | --- |
| `GET /cache/nix-cache-info` | Serve local gateway cache protocol metadata. |
| `GET /cache/{store-hash}.narinfo` | Look up peers and return verified output metadata. |
| `GET /cache/nar/{store-hash}/{nar-hash}.nar` | Stream the exact verified archive from an eligible peer. |
| `HEAD /cache/nix-cache-info` | Return gateway metadata headers without a body. |
| `HEAD /cache/{store-hash}.narinfo` | Look up output metadata without a response body. |
| `HEAD /cache/nar/{store-hash}/{nar-hash}.nar` | Validate archive availability without exporting its bytes. |
| `GET /store-cache/nix-cache-info` | Serve cache protocol metadata. |
| `GET /store-cache/{store-hash}.narinfo` | Serve valid local output metadata, signatures, and archive URL. |
| `GET /store-cache/nar/{store-hash}/{nar-hash}.nar` | Stream the exact local output after validating both hashes. |
| `HEAD /store-cache/nix-cache-info` | Return cache metadata headers without a body. |
| `HEAD /store-cache/{store-hash}.narinfo` | Return output metadata headers without a body. |
| `HEAD /store-cache/nar/{store-hash}/{nar-hash}.nar` | Return archive headers without exporting a body. |

Peer store routes require machine authorization; uploads are unsupported. The
gateway rewrites archive URLs without changing output integrity metadata.

## Interface decisions

Feature docs own the contracts: [admission scope](api-and-interfaces.md#operation-registry-and-scope),
[task/plan reviews](api-and-interfaces.md#reviews-and-concurrency),
[deployment defaults](deployment-and-rollback.md#preview-and-explicit-candidates),
and [guided deletion](gitops.md#change-retention-and-garbage-collection).
The [transport contract](runtime-configuration.md#service-ports) separates HTTP
console access from automatic backend TLS. Enrollment authentication retains its
[prototype gate](identity-and-trust.md#enrollment-http-exchange).

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
streaming cache gateway. Peer serving uses the machine TLS listener; NATS carries control
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
