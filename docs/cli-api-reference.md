# CLI and API reference

Interface based on [requirements.md](requirements.md); some commands and routes remain planned.
Commands below follow `nixmox`; HTTP routes are shown where applicable.

## global options

- `--endpoint URL`: connect to a daemon
- `--json`: compact JSON instead of indented JSON; streams use newline-delimited JSON
- `--config FILE`: read configuration; flags override environment and file values

## runtime

- `daemon [--ui-assets DIR]`: start the daemon with browser assets on the same listener; bundled assets are found automatically
- `ui` (alias `tui`): open the terminal console against `--endpoint URL`
- `health`: `GET /health`; daemon and cluster connection health

See [implementation.md](implementation.md#http-api-and-client) for current route support and runtime gaps; mappings below include unimplemented backends.

## cluster and hosts

- `cluster show`: `GET /cluster`; cluster identity
- `cluster create --name NAME`: `POST /cluster`; create on this host
- `cluster destroy --confirm ID`: `POST /cluster/destroy`; requires removal of other members
- `cluster recover --confirm ID`: `POST /cluster/recover`; metadata quorum rescue on a surviving server
- `host list`: `GET /hosts`
- `host show ID`: `GET /hosts/{id}`; show membership, endpoints, and daemon version
- `host promote ID --to STATUS [--force]`: `POST /hosts/{id}/promote`; promote a host to full or full-stable
- `host demote ID --to STATUS [--force]`: `POST /hosts/{id}/demote`; demote a host to full or pending
- `host remove ID [--force]`: `POST /hosts/{id}/remove`; remove a host; use force for offline hosts
- `removal list`: `GET /removals`
- `removal clear NODE_ID`: `DELETE /removals/{nodeId}`; allow a removed host to enroll again
- `host disks ID`: `GET /hosts/{id}/disks`
- `host configuration ID --configuration CONFIG_ID --version N`: `PUT /hosts/{id}/configuration`; assign a configuration to a host

## discovery and enrollment

- `discovery list`: `GET /discovery`
- `enrollment request --discovery ID`: `POST /enrollment/request`; request to join a discovered cluster
- `enrollment request --address URL`: `POST /enrollment/request`; request to join a cluster by endpoint
- `enrollment add --discovery ID --contact HOST_ID`: `POST /enrollment/request`; enroll a discovered node through a contact host
- `enrollment add --address URL --contact HOST_ID`: `POST /enrollment/request`; enroll a node by endpoint through a contact host
- `enrollment list`: `GET /enrollment`
- `enrollment show ID`: `GET /enrollment/{id}`; show request status and enrollment result
- `enrollment approve ID`: `POST /enrollment/{id}/approve`; approve a joining node's request
- `enrollment reject ID`: `POST /enrollment/{id}/reject`; reject and block an enrollment request
- `enrollment rejection list`: `GET /enrollment/rejections`
- `enrollment rejection clear ID`: `DELETE /enrollment/rejections/{id}`; clear an enrollment rejection

## repositories and credentials

- `repo list`: `GET /repos`
- `repo show ID`: `GET /repos/{id}`; show repository source, revision, and credential reference
- `repo add --file FILE`: `POST /repos`; track a repository from a definition file
- `repo update ID --file FILE`: `PATCH /repos/{id}`; update a tracked repository
- `repo remove ID`: `DELETE /repos/{id}`; stop tracking a repository
- `repo refresh ID`: `POST /repos/{id}/refresh`; fetch the latest tracked revision
- `credential list`: `GET /credentials`; metadata only
- `credential show ID`: `GET /credentials/{id}`; encrypted record
- `credential put --name NAME --type TYPE [--stdin]`: `POST /credentials`; hidden prompt or stdin; daemon encrypts before storage
- `credential remove ID`: `DELETE /credentials/{id}`; remove a credential
- `credential recipients`: `GET /credentials/recipients`; list authorized hosts and public age keys

## configurations and recipes

- `configuration list`: `GET /configurations`; list latest configuration versions
- `configuration show ID --version N`: `GET /configurations/{id}/versions/{version}`; show a pinned configuration and its collected values
- `configuration create --data JSON`: `POST /configurations`; Git configuration or realization of an exact recipe version
- `configuration update ID --data JSON`: `PUT /configurations/{id}`; JSON: `expectedVersion`, `fields`
- `configuration upgrade ID --version N --data JSON`: `POST /configurations/{id}/versions/{version}/upgrade`; JSON: `{id, version}` recipe reference; invalidates affected values
- `configuration target ID --version N --data JSON`: `POST /configurations/{id}/versions/{version}/target`; JSON: `{id, ref, sha}` target pin
- `configuration refresh ID`: `POST /configurations/{id}/refresh`; new Git-only version at the latest tracked revision
- `configuration observe ID --version N [--name NAME]`: `POST /configurations/{id}/versions/{version}/observe`; collect observations into a new version
- `configuration render ID --version N`: `POST /configurations/{id}/versions/{version}/render`; recipe-backed file preview and flake metadata; no publication
- `configuration publish ID --version N --mode MODE`: `POST /configurations/{id}/versions/{version}/publish`; planned; backend not implemented
- `recipe list`: `GET /recipes`; list latest reusable definitions
- `recipe show ID --version N`: `GET /recipes/{id}/versions/{version}`; show an immutable definition, not collected inputs
- `recipe create --data JSON`: `POST /recipes`; JSON: `sourceDefinition` or pinned `recipeSource`
- `recipe update ID --data JSON`: `PUT /recipes/{id}`; requires `expectedVersion`; tracked recipes fork, detached recipes append
- `recipe sync ID --version N`: `POST /recipes/{id}/versions/{version}/sync`; append changed definitions; configurations unchanged

## tasks

- `task list [--host ID] [--status STATUS] [--type TYPE]`: `GET /tasks`; filter flags are currently ignored by the API
- `task show ID`: `GET /tasks/{id}`; show task state, links, and results
- `task cancel ID`: `POST /tasks/{id}/cancel`; request task cancellation
- `task retry ID`: `POST /tasks/{id}/retry`; retry as a new linked task
- `task approve ID --output OUTPUT_ID`: `POST /tasks/{id}/approve`; approve the current task review
- `task output ID OUTPUT_ID`: `GET /tasks/{id}/outputs/{outputId}`
- `task logs ID`: `GET /tasks/{id}/logs`; read retained task logs
- `task logs ID --follow`: `GET /tasks/{id}/logs/stream`; replay and follow task logs over SSE
- `task watch ID`: `GET /tasks/{id}/events`; follow task state and progress over SSE

## builds and deployment

- `build --host ID --file FILE`: `POST /hosts/{id}/builds`; build a pinned Nix installable; currently limited to the connected daemon's host
- `deploy --host ID --configuration CONFIG_ID --version N [--build-host ID] [--approve]`: `POST /hosts/{id}/deployments`; build, review, and activate an exact configuration version
- `rollback --host ID`: `POST /hosts/{id}/rollbacks`; activate the previous retained generation

## power and daemon updates

- `host reboot ID [--force] [--evacuate]`: `POST /hosts/{id}/reboot`; reboot and verify the host returns
- `host shutdown ID [--force] [--evacuate]`: `POST /hosts/{id}/shutdown`; schedule host shutdown
- `host restart-daemon ID [--force] [--evacuate]`: `POST /hosts/{id}/restart-daemon`; restart the daemon and verify its new instance

## installation and boot media

- `installer list`: `GET /installers`
- `installer show ID`: `GET /installers/{id}`; show installer profile and pinned build inputs
- `installer register --file FILE`: `POST /installers`; register `kind` (`general` or `vm`), `system`, `targetRepo` (`id`, `ref`, full `sha`) and optional `flakeDir`
- `installer checks --host ID [--installer ID] [--prepare ID]`: `POST /hosts/{id}/installer-checks`; ranked compatible pairs, selected pair and separate execution readiness
- `install-prepare list`: `GET /install-prepare`; list installer-entry methods
- `install-finalize list`: `GET /install-finalize`; list installation-finalization methods
- `installer launch --host ID [--installer ID] [--prepare ID] [--build-host ID]`: `POST /hosts/{id}/launch-installer`; defaults to the best compatible registered pair
- `install-finalize checks --host ID --root PATH --system STORE_PATH [--finalize ID]`: `POST /hosts/{id}/install-finalize-checks`; general finalizer compatibility
- `install-finalize run --host ID --root PATH --system STORE_PATH [--finalize ID]`: `POST /hosts/{id}/install-finalize`; submit finalization of a mounted installed root
- `install --host ID --configuration CONFIG_ID --version N [--build-host ID]`: `POST /hosts/{id}/installations`; install an exact configuration version from the installer environment
- `media create --host ID --device PATH --installer INSTALLER_ID [--build-host ID]`: `POST /hosts/{id}/boot-media`; build and write an installer to the selected drive

Installer registration/checks are available for the connected daemon. Launch/finalize execution currently returns 503 until client-only startup, persistent-task recovery and demotion/state handoff are wired. Remote host/build dispatch is not implemented. Method IDs are `prepare-kexec` and `finalize-general`; omitting the flags selects them automatically.

## local networking

- `network wifi list [--interface NAME]`: `GET /network/wifi`; list Wi-Fi networks visible to the connected daemon
- `network wifi connect --ssid SSID [--interface NAME]`: `POST /network/wifi/connect`; connect to Wi-Fi, prompting for a password

## observability and cache

- `host metrics ID --follow`: `GET /hosts/{id}/metrics/stream`; follow live host metrics over SSE
- `host logs ID`: `GET /hosts/{id}/logs`; read retained host logs
- `host logs ID --follow`: `GET /hosts/{id}/logs/stream`; replay and follow host logs over SSE
- `cache show`: `GET /cache-info`; show cache gateway, public signing key, and peers

## daemon and nix protocol routes

- `POST /internal/enrollment/request`: submit a joining node's CSR to a cluster contact
- `POST /internal/enrollment/add`: invite an unjoined node and receive its CSR
- `POST /internal/enrollment/confirm`: deliver the issued node certificate
- `GET /removals/{nodeId}/proof`: retrieve signed evidence of forced removal
- `GET /cache/*`: fetch Nix cache artifacts through the peer gateway
- `GET /store-cache/*`: fetch artifacts from this host's local Nix cache
