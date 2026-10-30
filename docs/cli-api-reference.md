# CLI and API reference

Proposed interface based on [requirements.md](requirements.md); not all commands or routes are implemented.

## Global options

| Option | Description |
| --- | --- |
| `--endpoint URL` | Connect to a daemon. |
| `--json` | Print structured JSON output. |

## Runtime

| CLI | API | Description |
| --- | --- | --- |
| `daemon` | — | Start the Nixmox daemon. |
| `tui` | — | Open the terminal interface. |
| `health` | `GET /health` | Show daemon and cluster connection health. |

## Cluster and hosts

| CLI | API | Description |
| --- | --- | --- |
| `cluster show` | `GET /cluster` | Show cluster identity. |
| `cluster create --name NAME` | `POST /cluster` | Create a cluster on this host. |
| `cluster destroy --confirm ID` | `POST /cluster/destroy` | Destroy the cluster after removing other members. |
| `cluster recover --confirm ID` | `POST /cluster/recover` | Request metadata quorum rescue on a surviving server. |
| `host list` | `GET /hosts` | List cluster hosts. |
| `host show ID` | `GET /hosts/{id}` | Show membership, endpoints, and daemon version. |
| `host promote ID --to STATUS [--force]` | `POST /hosts/{id}/promote` | Promote a host to full or full-stable. |
| `host demote ID --to STATUS [--force]` | `POST /hosts/{id}/demote` | Demote a host to full or pending. |
| `host remove ID [--force]` | `POST /hosts/{id}/remove` | Remove a host; use force for offline hosts. |
| `removal list` | `GET /removals` | List removed hosts. |
| `removal clear NODE_ID` | `DELETE /removals/{nodeId}` | Allow a removed host to enroll again. |
| `host disks ID` | `GET /hosts/{id}/disks` | List a host's drives. |
| `host configuration ID --configuration CONFIG_ID` | `PUT /hosts/{id}/configuration` | Assign a configuration to a host. |

## Discovery and enrollment

| CLI | API | Description |
| --- | --- | --- |
| `discovery list` | `GET /discovery` | List discovered nodes and clusters. |
| `enrollment request --discovery ID` | `POST /enrollment/request` | Request to join a discovered cluster. |
| `enrollment request --address URL` | `POST /enrollment/request` | Request to join a cluster by endpoint. |
| `enrollment add --discovery ID --contact HOST_ID` | `POST /enrollment/request` | Enroll a discovered node through a contact host. |
| `enrollment add --address URL --contact HOST_ID` | `POST /enrollment/request` | Enroll a node by endpoint through a contact host. |
| `enrollment list` | `GET /enrollment` | List enrollment requests. |
| `enrollment show ID` | `GET /enrollment/{id}` | Show request status and enrollment result. |
| `enrollment approve ID` | `POST /enrollment/{id}/approve` | Approve a joining node's request. |
| `enrollment reject ID` | `POST /enrollment/{id}/reject` | Reject and block an enrollment request. |
| `enrollment rejection list` | `GET /enrollment/rejections` | List enrollment rejections. |
| `enrollment rejection clear ID` | `DELETE /enrollment/rejections/{id}` | Clear an enrollment rejection. |

## Repositories and credentials

| CLI | API | Description |
| --- | --- | --- |
| `repo list` | `GET /repos` | List tracked repositories. |
| `repo show ID` | `GET /repos/{id}` | Show repository source, revision, and credential reference. |
| `repo add --file FILE` | `POST /repos` | Track a repository from a definition file. |
| `repo update ID --file FILE` | `PATCH /repos/{id}` | Update a tracked repository. |
| `repo remove ID` | `DELETE /repos/{id}` | Stop tracking a repository. |
| `repo refresh ID` | `POST /repos/{id}/refresh` | Fetch the latest tracked revision. |
| `credential list` | `GET /credentials` | List credential metadata. |
| `credential show ID` | `GET /credentials/{id}` | Show an encrypted credential record. |
| `credential put --name NAME --type TYPE [--stdin]` | `POST /credentials` | Submit a raw secret from a hidden prompt or stdin; the daemon encrypts it before storage. |
| `credential remove ID` | `DELETE /credentials/{id}` | Remove a credential. |
| `credential recipients` | `GET /credentials/recipients` | List authorized hosts and public age keys. |

## Configurations and recipes

| CLI | API | Description |
| --- | --- | --- |
| `configuration list` | `GET /configurations` | List registered configurations. |
| `configuration show ID` | `GET /configurations/{id}` | Show a configuration and its source pin. |
| `configuration refresh ID` | `POST /configurations/{id}/refresh` | Update the configuration to its latest tracked revision. |
| `recipe list` | `GET /recipes` | List recipes. |
| `recipe show ID --version N` | `GET /recipes/{id}/versions/{version}` | Show a recipe version and collected inputs. |
| `recipe create --data JSON` | `POST /recipes` | Create a recipe from fields in the JSON request body. |
| `recipe update ID --data JSON` | `PUT /recipes/{id}` | Apply changes from the JSON request body as a new recipe version. |
| `recipe observe ID --version N [--name NAME]` | `POST /recipes/{id}/versions/{version}/observe` | Collect or refresh recipe observations. |
| `recipe render ID --version N` | `POST /recipes/{id}/versions/{version}/render` | Render a recipe into a pinned repository worktree. |
| `recipe publish ID --version N --mode MODE` | `POST /recipes/{id}/versions/{version}/publish` | Publish by direct commit or pull request. |

## Tasks

| CLI | API | Description |
| --- | --- | --- |
| `task list [--host ID] [--status STATUS] [--type TYPE]` | `GET /tasks` | List and filter tasks. |
| `task show ID` | `GET /tasks/{id}` | Show task state, links, and results. |
| `task cancel ID` | `POST /tasks/{id}/cancel` | Request task cancellation. |
| `task retry ID` | `POST /tasks/{id}/retry` | Retry as a new linked task. |
| `task approve ID --output OUTPUT_ID` | `POST /tasks/{id}/approve` | Approve the current task review. |
| `task output ID OUTPUT_ID` | `GET /tasks/{id}/outputs/{outputId}` | Show a structured task output. |
| `task logs ID` | `GET /tasks/{id}/logs` | Read retained task logs. |
| `task logs ID --follow` | `GET /tasks/{id}/logs/stream` | Replay and follow task logs over SSE. |
| `task watch ID` | `GET /tasks/{id}/events` | Follow task state and progress over SSE. |

## Builds and deployment

| CLI | API | Description |
| --- | --- | --- |
| `build --host ID --file FILE` | `POST /hosts/{id}/builds` | Build a pinned Nix installable. |
| `deploy --host ID --configuration CONFIG_ID [--build-host ID] [--approve]` | `POST /hosts/{id}/deployments` | Build, review, and activate a configuration. |
| `deploy --host ID --recipe RECIPE_ID --version N [--build-host ID] [--approve]` | `POST /hosts/{id}/deployments` | Build, review, and activate a recipe version. |
| `rollback --host ID` | `POST /hosts/{id}/rollbacks` | Activate the previous retained generation. |

## Power and daemon updates

| CLI | API | Description |
| --- | --- | --- |
| `host reboot ID [--force] [--evacuate]` | `POST /hosts/{id}/reboot` | Reboot and verify the host returns. |
| `host shutdown ID [--force] [--evacuate]` | `POST /hosts/{id}/shutdown` | Schedule host shutdown. |
| `host restart-daemon ID [--force] [--evacuate]` | `POST /hosts/{id}/restart-daemon` | Restart the daemon and verify its new instance. |

## Installation and boot media

| CLI | API | Description |
| --- | --- | --- |
| `installer list` | `GET /installers` | List installer definitions. |
| `installer show ID` | `GET /installers/{id}` | Show installer hardware requirements and build inputs. |
| `install-prepare list` | `GET /install-prepare` | List installer-entry methods. |
| `install-finalize list` | `GET /install-finalize` | List installation-finalization methods. |
| `installer launch --host ID --installer INSTALLER_ID [--build-host ID]` | `POST /hosts/{id}/launch-installer` | Build and boot an installer on a host. |
| `install --host ID --configuration CONFIG_ID [--build-host ID]` | `POST /hosts/{id}/installations` | Install a configuration from the installer environment. |
| `install --host ID --recipe RECIPE_ID --version N [--build-host ID]` | `POST /hosts/{id}/installations` | Install a recipe version from the installer environment. |
| `media create --host ID --device PATH --installer INSTALLER_ID [--build-host ID]` | `POST /hosts/{id}/boot-media` | Build and write an installer to the selected drive. |

## Local networking

| CLI | API | Description |
| --- | --- | --- |
| `network wifi list [--interface NAME]` | `GET /network/wifi` | List Wi-Fi networks visible to the connected daemon. |
| `network wifi connect --ssid SSID [--interface NAME]` | `POST /network/wifi/connect` | Connect to Wi-Fi, prompting for a password. |

## Observability and cache

| CLI | API | Description |
| --- | --- | --- |
| `host metrics ID --follow` | `GET /hosts/{id}/metrics/stream` | Follow live host metrics over SSE. |
| `host logs ID` | `GET /hosts/{id}/logs` | Read retained host logs. |
| `host logs ID --follow` | `GET /hosts/{id}/logs/stream` | Replay and follow host logs over SSE. |
| `cache show` | `GET /cache-info` | Show cache gateway, public signing key, and peers. |

## Daemon and Nix protocol routes

| API | Description |
| --- | --- |
| `POST /internal/enrollment/request` | Submit a joining node's CSR to a cluster contact. |
| `POST /internal/enrollment/add` | Invite an unjoined node and receive its CSR. |
| `POST /internal/enrollment/confirm` | Deliver the issued node certificate. |
| `GET /removals/{nodeId}/proof` | Retrieve signed evidence of forced removal. |
| `GET /cache/*` | Fetch Nix cache artifacts through the peer gateway. |
| `GET /store-cache/*` | Fetch artifacts from this host's local Nix cache. |
