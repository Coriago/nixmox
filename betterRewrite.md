# Nixmox Feature guide

## membership

Status
- unjoined: hosts not in a cluster but running nixmox
- pending: hosts in a cluster but configuration has not assigned and deployed
- full: hosts that participate in cluster ownership
- removed: hosts explicitly removed from the cluster. membership can be reinstated.

Membership statuses exist to determine which membrs should have cluster ownership so the cluster can stay stable.
Newly joined members are considered pending since they are often followed by fresh installation.
Until they have chosen a configuration and deployed, we don't want to rely on them. 
The host that starts the cluster is a full member automatically.
The last full member cannot be removed from the cluster without adding another full member or destruction of the cluster.

## cluster creation
- clusters can be created from any interface once the nixmox service is running
- unjoined hosts have the option to join an existing cluster or create a new one
- clusters can only be created by unjoined hosts
- clusters are assigned a name, a persistent id and a CA
- cluster creator is automatically a full member

## cluster destruction
- to destroy a cluster, all members must be removed from the cluster until the last full member.
- cluster destruction can then be performed and requires confirmation from the user.
- after destruction the last member becomes unjoined

## forced membership actions
- hosts can be forced to promote to full or forced to demote to pending
- only full members can send forceful memebership actions
- force demotion can not happen on the last full member
- promotions/demotions are queued one at a time to avoid race conditions
- these actions are not a recommended path, they are here for testing and fallback scenarios.

### force removal
- hosts can also me force removed from the cluster when they are no longer online
- if a host that was force removed comes online, it will query a force removal http endpoint after it failed to connect.
- the endpoint will respond if the host was removed and the node will change its state to unjoined.
- the removal payload from the cluster will be signed by the cluster so the node can confirm its validity
- a removal can also be removed to let the node rejoin the cluster

## node init
- when nixmox starts for the first time on a host, it generates a unique node id that persists
- a tls secret is also created and persisted

## gitops
- configure one or more git repositories
- pull on cadence and notify other hosts when updates are available
- used for templates, deployments
- tracked repos can be added or removed through the interface
- credentials can be associated to a tracked repo for publishing to the repo or private repo auth

Tracked repository without credentials:

```yaml
id: repo1
name: nixos-configs
url: https://github.com/example/nixos-configs.git
credentialId: null
ref: main
sha: "123412341234"
```

Tracked repository with credentials:

```yaml
id: repo2
name: private-configs
url: git@github.com:example/private-configs.git
credentialId: githubCredentialId
ref: main
sha: "567856785678"
```

## credentials
- credentials are secret values stored in the cluster and used for cluster CA, git, sops or nix signing.
- all full members get access to cluster credentials
- secrets in credentials are encrypted using sops and age keys generated from the ssh host keys
- the encrypted sops files and sops metadata are stored in the nats store
- only full members have access to decrypt and update the secrets.
- everytime a full member is added, their public key is added to sops and keys are updated
- full members that are demoted or leave are removed
- credential sops is initialized on cluster creation and stores the cluster CA and default nix signing key
- actions that require credentials will be performed on hosts that have access to the credentials
- EX: publishing a recipe, updating sops in for a configuration or enrolling another host

##  enrollment
- process where new hosts running nixmox join a cluster
- Enrollment establishes trust; it does not require a previously known
 cluster or node identity.
- unjoined nodes can request to join a cluster. requests need confirmation
- clusters that send enrollment requests to unjoined nodes need no confirmation.
- a decision to join happens either way giving priority to the cluster for authoritative trust.
- enrollment requests require a discovery id or endpoint address to the cluster or node
- removed nodes cannot join the cluster

Key exchange
This is varient of a typical CSR flow
1. unjoined nodes will send a csr request to a cluster node with their node id, name, and public key
unjoined -(name,id,public-key)-> cluster
2. the cluster will approve or reject the request. 
- rejections are saved so further requests by that endpoint are blocked. A rejection can still be removed if accidental.
- approval will result in a signed certificate
3. cluster sends a confirmation back to the sender with the signed cert and public CA.
4. unjoined node will configure its nats with its private key, the new issued certificate and the public CA then join the cluster.

cluster flow: 
1. if the cluster is adding the node, it will send a http request to the unjoined node for a CSR
2. it then issues a cert for the CSR and send the info to the unjoined node's confirmation endpoint

##  discovery
- make enrollment easier by letting nodes find clusters and clusters find nodes
- broadcast happens on common port
- sends out available endpoints, node id/cluster id and name
- this is not required to add hosts, it is just to make it easier

## configurations
- configurations is a registry of nixos configurations
- they can be from git repos or recipes rendered inside of a repo
- remote configurations are automatically registered from tracked repos
- they do not have to be commited to the remote repo when using a recipe
- hosts must select a configuration from the registry to become full members.

```yaml
id: cedar-uuid
name: cedar
targetRepo:
    id: repo1
    ref: main
    sha: "123412341234"
flakeDir: .
flakeConfiguration: cedar

```

##  configuration recipes
- configuration can be generated on the fly using a recipe to make it easier to setup new hosts.
- configuration recipes are designed to be rendered into a their git source when the host selects the recipe.
- recipes are versioned and any new modification means a new version is produced. Therefore updating a recipe does not 
affect a host consuming that recipe. Hosts must update to the new recipe version to consume.
- updating the git source to newer versions means the recipe is generated again over that source on new worktree dir, not pulled in the existing recipe render.
- recipes that are renderable store all of their inputs, templates, forms and observations.
- recipes are designed to be partial so they can be completed incrementally as observations are resolved or user inputs given.
- a recipe using a soure recipe can have its definition edited but it will create a new recipe and version that is no longer attached to the source and will not be updated from it. the original recipe will still exist and can be updated with new versions.
- a repo recipe can set definition fields: fields, observations, outputs, flakeDir, flakeConfiguration

Here is recipe stored at runtime that uses a recipe from a repo

```yaml
id: "recipe1-uuid"
version: 6
canRender: true
targetRepo:
  id: repo1
  ref: main
  sha: "123412341234"
recipeSource:
  id: repo1
  ref: main
  sha: "hakevm12"
  path: .nixmox/recipes/myrecipe.yaml
sourceDefinition:
    flakeDir: .
    flakeConfiguration: "{{ .Name }}"
#... the rest from the source recipe below
collected:
  fields:
      Name: mycoolhost
      Username: helios
      Modules:
        - base
        - gaming
        - development
      Disk: "/dev/nvme0n1"
      TargetHost: node1_id
  observations:
    hosts:
        collectedAt: dtm
        output:
        - id: node1_id
          name: node1
        - id: node2_id
          name: node2
    flakeModules:
        collectedAt: dtm
        repo:
          id: repo1
          ref: main
          sha: "123412341234"
        output: [ "base", "development", "gaming", "workstation", "tailscale" ]
    ext4Disk:
        collectedAt: dtm
        repo:
          id: repotemplateId
          ref: main
          sha: dkewidkw312l
        output: <raw output of file here>
    disks:
        collectedAt: dtm
        host: node1_id
        output: 
        - device: "/dev/nvme0n1"
          size: "128GB"
        - device: "/dev/sda"
          size: "1023GB"
    hardwareConfig:
        collectedAt: dtm
        host: node1_id
        output: <output of nixos-generate-config --no-filesystems>
    sopskey:
        collectedAt: dtm
        host: node1_id
        output: "age1ql3z7hjy54pw3hyww5ayyfg7zqgvc7w3j2elw8zmrj2kg5sfn9aqmcac8p"
    configurationTemplate:
        repo:
          id: repo1
          ref: main
          sha: "hakevm12"
        path: .nixmox/recipes/configuration.gotmpl
        output: <raw text of file>
    hardwareTemplate:
        repo:
          id: repo1
          ref: main
          sha: "hakevm12"
        path: .nixmox/recipes/hardware.gotmpl
        output: <raw text of file>
```

Here is the configuration recipe in repo1
.nixmox/recipes/myrecipe.yaml

```yaml
flakeDir: .
flakeConfiguration: "{{ .Name }}"
fields:
  Name:
    type: text
  Modules:
    type: multiselect
    values: "{{ .observations.flakeModules }}"
  Username:
    type: text
  Disk:
    type: select
    values: "{{ .observations.disks }}"
    optionValue: "{{ .device }}"
    optionLabel: "{{ .device }} {{ .size }}"
  TargetHost:
    type: select
    values: "{{ .observations.hosts }}"
    optionValue: "{{ .id }}"
    optionLabel: "{{ .name }} ({{ .id }})"
observations:
  hosts:
    type: clusterHosts
  flakeModules:
    type: repoCommand
    repo: "{{ .targetRepo }}"
    cmd: nix eval --json .#modules.nixos --apply builtins.attrNames
  ext4Disk:
    type: repoFile
    repo: 
        url: https://github.com/nix-community/disko-templates.git
        ref: main
    path: single-disk-ext4/disko-config.nix
  disks:
    type: hostDisks
    host: "{{ .TargetHost }}"
  hardwareConfig:
    type: hostHardware
    noFileSystems: true
    host: "{{ .TargetHost }}"
  sopskey:
    type: hostSSHtoAgeKey
    host: "{{ .TargetHost }}"
  configurationTemplate:
    type: repoFile
    repo: "{{ .recipeSource }}"
    path: .nixmox/recipes/configuration.gotmpl
  hardwareTemplate:
    type: repoFile
    repo: "{{ .recipeSource }}"
    path: .nixmox/recipes/hardware.gotmpl
outputs:
  "modules/hosts/{{ .Name }}/configuration.nix":
    template: "{{ .observations.configurationTemplate }}"
  "modules/hosts/{{ .Name }}/hardware.nix":
    template: "{{ .observations.hardwareTemplate }}"
  "modules/hosts/{{ .Name }}/disko.nix":
    template: |
      {
          configurations.nixos.{{ .Name }} = { ... }: {
            imports = [ ../../../vendor/disko/single-disk-ext4.nix ];
            disko.devices.disk.main.device = "{{ .Disk }}";
          };
      }
  vendor/disko/single-disk-ext4.nix:
    raw: "{{ .observations.ext4Disk }}"
  .sops.yaml:
    updateSops:
      key: "{{ .observations.sopskey }}"
      anchorValue: "{{ .Name }}"
      rules:
        - secrets/secrets.yaml
```

.nixmox/recipes/hardware.gotmpl
```nix
{
    configurations.nixos.{{ .Name }} = {{ .observations.hardwareConfig }};
}
```


.nixmox/recipes/configuration.gotmpl
```nix
{config, ...}: {
    meta.hosts.{{ .Name }} = {
        username = "{{ .Username }}";
        stateVersion = "26.05";
    };
    configurations.nixos.{{ .Name }} = {
        imports = [
            {{- range .Modules }}
            config.flake.modules.nixos.{{ . }}
            {{- end }}
        ];
    };
}
```


final render:
modules/hosts/mycoolhost/configuration.nix
```nix
{config, ...}: {
    meta.hosts.mycoolhost = {
        username = "helios";
        stateVersion = "26.05";
    };
    configurations.nixos.mycoolhost = {
        imports = [
            config.flake.modules.nixos.base
            config.flake.modules.nixos.gaming
            config.flake.modules.nixos.development
        ];
    };
}
```

modules/hosts/mycoolhost/disko.nix
```nix
{
    configurations.nixos.mycoolhost = { ... }: {
        imports = [ ../../../vendor/disko/single-disk-ext4.nix ];
        disko.devices.disk.main.device = "/dev/nvme0n1";
    };
}
```

modules/hosts/mycoolhost/hardware.nix
```nix
{
    configurations.nixos.mycoolhost = <output of nixos-generate-config --no-filesystems>;
}
```

The hardware expression above is a placeholder for the collected module, whose
contents are not specified in this example.

vendor/disko/single-disk-ext4.nix contains the unchanged file contents captured in
`collected.observations.ext4Disk.output`.

.sops.yaml
```yaml
keys:
    &mycoolhost age1ql3z7hjy54pw3hyww5ayyfg7zqgvc7w3j2elw8zmrj2kg5sfn9aqmcac8p
    #...

creation_rules:
    - path_regex: secrets/secrets.yaml
      key_groups:
        - age:
          - *mycoolhost
          #...
```

Important Notes:
- go style interpolation can is used throughout the recipe to resolve values
- some top level fields cannot use interpolation but other fields can typically use them.
  - version, id, targetRepo, recipeSource, collected, canRender
- fields are for requesting user input
- observations is for bringing in external information to complete the recipe
- outputs defines how the files/modifications are rendered.
- collected contains all of the information realized by performing the observations and taking user input
- once all fields and obersvations have values, the recipe can be rendered
- observations can be refreshed, which would invalidate other inputs if the value would have changed.
- for example, refreshing hostDisks could output a list that no longer contains the chosen drive.
- Changing TargetHost invalidates its hardware, disks, and public-key
   observations even if the new host exposes the same disk path.
- if a field uses an observation that isn't known yet, it is in a loading state until the observation can be completed.
- in the example, modules/hosts/** is automatically imported and the values in configurations.nixos are merged automatically
- repos referenced in observations are automatically added into tracked gitops repos if they are not already.
- repo ids and shas are filled in for collected observations if they are not provided in the template.
- cycles are not allowed in the recipe. an observation cannot depend on a input that depends on the same observation.

field types are:
text, select, multiselect, boolean

observation types:
clusterHosts - current member hosts in the cluster
clusterRepos - current repos attached to the cluster
clusterCreds - current creds attached to the cluster, only ids not secret values
hostDisks - fetchs the list of disks from the host
hostHardware - fetches the generated hardware config for the host
hostSSHtoAgeKey - fetches the public age key based on the host ssk key
repoCommand - runs a command in a repo
repoFile - gets the content of a file in a repo. path in the repo input is not used.

output render methods:
template: a go template that can use field values or templates
raw: the raw content to set the file to
updateSops: runs a process that updates the sops file with the key

## recipe publication
- a recipe can be finalized and pushed to the remote repo
- it is first rendered into the latest version of the target repo's ref and a commit is created
- publish can be commited directly to the target ref or a pull request can be opened into the target ref
- credentials for the remote repo must be set in order to publish

## tasks
- tasks are a generic concept for performing tracked operations on hosts
- they allow for easy visibility across the cluster or a host to see what has failed, what is currently running etc.
- tasks can run in parallel or they can be scheduled after another task completes successfully
- tasks can be cancelled or retried
- task retries should create another task so the original can be preserved, but a link to the original task should be shown
- any task kicked off by another task should appear in task links
- retry is not automatic, it must be manually triggered
- tasks can be extended to have more fields
- a build task would be a task that performs a nix build and may report package updates, download speeds and other information.
- all tasks are designed to integrate logs which can be streamed while they are running and saved for historical lookup.
- tasks may be host specific and should only be updated by that host such as a nix build.
- the only time a running task is updated by another host is during global cleanup
- the task manager for a host will routinely check what tasks are running against what local task runners are running. if a task runner had crashed and failed to report status, then the task manager will mark the task as unknown.
- global task cleanup will happen periodically to cleanup tasks for other hosts. a task will be set to unknown status if the task is running for over 30 mins since the host of the task went offline. this is to ensure no tasks stay running indefinetly.
- tasks can also have a manual review state where they halt execution and await approval before continuing. The approval can happen in the middle of the task, it does not have to be the beginning

### task log adapters
- tasks can use log adapters to parse logs into task progress telemetry
- the raw log will still be streamed, the adapter will parse independently during task execution 
- an example adapter would be nix builds which can send download speed, number of packages built, progress of packages being downloaded etc.

## host telemetry, progress updates and log streams
- live telemetry and logs are sent from all hosts to display in any interface connected to any host daemon
- information is sent via nats back to the daemon which forwards the information to the interface
- logs streams must keep full history when streaming batached logs using replay
- logs are stored for historical viewing and will have a ttl of 5 days by default.
- telemetry and progress updates do not need full history or replay, the most up to date information is favored here.

## cluster cache
- a cluster cache is used to transfer built artifacts across hosts
- the cache is a gateway proxy to all other peer caches so only one url is used
- the gateway proxy uses bounded timeouts and fail over to other peers to try and prevent stalls and failures in case peers go down
- gateway proxy will try all peers to see if they have the package cached
- /cache is the gateway and /store-cache is the local cache on the host the gateway queries.
- the local cache is simple and embedded into the app to keep nixmox self contained.
- cluster cache will use the generated cache signing key if one is not configured.
- the private cache key is added as a cluster credential to distribute to all full members
- nixmox will add the the cache proxy and public key as extra substituters and trusted keys via cli flags on build actions
- the nixmox module can configure nix settings to trust the cache proxy and the keys

##  builds
- nix build tasks are used to build nixos configurations or packages on a host
- you can schedule builds on any host from the interface allowing for cross host building
- nix builds should configure a log adapter inspired by nix output manager to send build telemetry

## deployment
- a deployment consists of a build, a review and finally activation
- a deployment targets
  - configuration or recipe at a specific pin on the targetRepo.
  - targetHost
- deploying the latest changes means getting the lastest sha from the targetRepo ref and updating the sha on the configuration
- optionally another host can be chosen to prebuild the configuration for the target machine
- when the build and deploy runs on the target machine it stops before activation and awaits approval
- diff review is presented showing package version changes, added and removed packages
- diff review is not just a log, it is a output that is attached to the task and can be displayed cleanly in the interface with buttons to approve
- confirmation of the diff review triggers the task to continue and activation of the build 
- if host is pending, it is promoted to full on successfull deployment
- a check box for auto approve is next to the deploy button if you want to automatically approve the review
- --approve can be used for the cli

### deployment diff review
- compare current generation to new generation
- save store paths of each in the review
- after approval, just activate do not build again

### rollback
- rollback to the last generation can be performed on the fly
- rollback to specific generations will not be done in v1 right now
- rollback can be performed multiple times going further back as long as the generation has not been garbage collected

### fresh deployments
- a fresh deployment task includes disk formatting and deployment onto the associated disks in disko
- must first be in an installer state to do fresh deployment
- fresh deployment does not transition the node into an installer state if it is not in one, that is a seperate action
- no reviews for fresh deployments, built artifacts must transfer into the target drive
- must transfer persisted ids into the destination installation from the installer environment
  - node id
  - node tls keys 
  - cluster issued keys, cluster CA, cluster id and connection info
  - wifi credentials
  - ssh host key
  - the task id of the deployment
- fresh install task is complete once the reboot is performed and post install validation complete
- post install validation ensures connection to cluster established, expected configuration generation is active and ids are set
- host is promoted to full membership


##  installers
- bare installation environment containing nixmox daemon
- installer starts up into the nixmox client tui with installer flag
- a network connectivity check is performed on network devices
- if no connection, prompt is shown that the device needs to be wired or connected via wifi if that interfaces is available
- built for the corresponding hardware, x86, arm, or hardware specific like raspberry pi
- build can happen on other hosts for another hosts hardware
- incremental deployments can not run in an installer, only fresh deployments
- new ssh host keys, node ids, and node keys are generated on each installer start unless started by a launcher

### installer launchers
- an installer launcher transitions a running machine with nixmox into an installer state off of the runtime drives
- the running host will trigger a demotion action to pending if it was a full member, demotion must finish before launching
- the correct launcher is chosen based on hardware
- kexec is used if available on the host
- rebooting into a connected boot device is an alternative when kexec is not possible
- efi boot is used on uefi machines that do not have kexec
- raspberry pi eeprom may be used to transition to an sd card when using full nvme boot
- raspberry pi uboot is another option if sd card is used for boot and nvme as root
- launchers must ensure these values persist from the old environment
  - node id
  - node tls keys
  - cluster issued keys, cluster CA, cluster id and connection info
  - wifi credentials (ensure network connection is maintained)
  - ssh host key

### local network management
- hosts booted into an installer environment may need to connect to wifi network to talk with a cluster
- a utility is bundled into the nixmox interface to manage a network connection for the local host
- the nixmox local network utility can search for and show available wifi networks and signal strength
- a network can be selected and a password entered to connect

## boot media
- sd cards or usb devices can be formatted with a bootable installer
- in the interface, a user can select which host the drive is connected to, default is the daemon the interface is connected to
- select a drive from a dropdown
- choose the target host for the installer and choose a which host will do the initial build. default is the current host
- then execute the build of the installer and formatting of the drive
- hosts in pending or in an installer state can not create media
- no confirmation necessary on drive selected


## NATS quorum
- when there are less than 3 full members, the cluster only has one host for storage replicas
- only full members serve replicas. pending members cannot host replicas
- once 3 full members are reached, the cluster automatically snapshots the old data and transitions to jetstream with the number of replicas matching the number of full members
- number of replicas follows the number of full members both up and down
- if full members drop below 3, the cluster will automatically transition back into a single replica state, choosing any remaining full member to host the replicas
- full members that go offline do not affect the transition logic, they must be demoted or removed to change the number of full memebers
- only operations that use jetstream backed storage are halted



## security posture v1
- backend systems use encrypted traffic when talking with nats.
- daemon to daemon traffic uses http but only this is only for discovery, enrollment, cache and force removal
- interfaces cannot talk directly to nats servers, all traffic must proxy through the api.
- nats tls and secrets are generated and managed internally, no user setup 
- there is a basic authorization policy for nats
    - actions for specific hosts can only be performed by that host's local daemon not remote daemons
    - pending nodes cannot add new members or remove members

(cli, tui, gui) <-(http/sse)-> daemon api <-tls-> nats server
                                  |                 |
                                 http              tls 
                                  |                 |
                               daemon api <-tls-> nats server

- no user or role management. access to the api is limited to network connectivity
- interfaces are not limited to the local daemon api, they can talk directly to other daemon interfaces over the network.
