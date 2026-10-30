# Nixmox Requirements guide

## membership

Status
- unjoined: hosts not in a cluster but running nixmox
- pending: hosts in a cluster but configuration has not assigned and deployed
- full: trusted hosts with full permissions and cache hosting; connect as NATS clients without hosting cluster servers
- full-stable: full permissions and cache hosting plus NATS/JetStream servers and replicas; intended for reliably available hosts
- removed: hosts explicitly removed from the cluster. membership can be reinstated.

Unless a specific status or transition is named, "full members" means both full and full-stable members.
Both have the same credential access and administrative rights. Only full-stable members
carry server, replica, and quorum responsibilities; client-only full members can sleep or disconnect
without changing server membership. Sleeping does not automatically change either status.
Newly joined members are pending until a configuration is assigned and deployed.
The host that starts the cluster is full-stable automatically.
The last full-stable member cannot be removed or demoted without another full-stable member or destruction of the cluster.

**Membership Record**<br>
Location: NATS
```yaml
nodeId: node1
name: cedar
clusterId: cluster1
status: full-stable
environment: installed # installed or installer; independent of membership status.
agePublicKey: "<SSH-derived host age public key>"
configurationId: cedar-uuid
daemon:
  version: "0.1.0"
  startedAt: "2026-10-29T18:00:00Z"
  updateReady: true
  availableVersion: "0.2.0"
```

## cluster creation
- clusters can be created from any interface once the nixmox service is running
- unjoined hosts have the option to join an existing cluster or create a new one
- clusters can only be created by unjoined hosts
- clusters are assigned a name, a persistent id and a CA
- cluster creator is automatically a full-stable member
- creator starts the first nats jetstream server and bootstrap jetstream server

**Cluster identity record**<br>
Location: NATS
```yaml
id: cluster1
name: homelab
createdByNodeId: node1
ca:
  certificate: "<public CA certificate PEM>"
  credentialId: cluster-ca
```

## cluster destruction
- to destroy a cluster, all other members must be removed until only the last full-stable member remains.
- cluster destruction can then be performed and requires confirmation from the user.
- after destruction the last member becomes unjoined, all nats servers shutdown

## node init
- when nixmox starts for the first time, it generates a unique node id that persists
- a tls secret is also created and persisted
- it does not start a nats jetstream server

**node persistent state record**<br>
Location: Local host (persistent)
```yaml
nodeId: node1
name: cedar
clusterId: null
status: unjoined
environment: installed
identity:
  privateKeyFile: /var/lib/nixmox/node.key
resumeTaskIds: [] # Persist reboot, daemon-restart, launch, or install task IDs before transition.
```

## node promotion
- nodes are promoted from pending to full automatically after a configuration is assigned and deployed successfully
- the node's public age key generated from its ssh host key is added to the credential store by another full member
- full membership rights are given; the node remains a NATS client connected to a full-stable member
- start the binary cache using the Nix signing key and add the node to the cache peer list
- member status persists across daemon restarts so the node knows which services to start

**Node promotion task (base task specialization)**<br>
Location: NATS
```yaml
id: promote1
tasktype: node-promotion
hostId: node1 # Full member coordinating the transition for node2.
status: succeeded
concurrency:
  exclusiveResources: ["cluster:cluster1:membership", "host:node2:system"]
inputs:
  nodeId: node2
  toStatus: full
  force: false
context:
  previousStatus: pending
result:
  membershipStatus: full
```

### promotion to full-stable
- promotion from full to full-stable is an explicit choice, not an automatic result of deployment
- verify direct route connectivity with every existing full-stable member over an available LAN or overlay network
- start the node's NATS/JetStream server with its credentials and wait for it to join the cluster
- connect the node's NATS client to its embedded server
- update replication and bootstrap placement using the new full-stable count
- existing cache hosting continues unchanged
- persist full-stable status; credential access and administrative permissions are unchanged

**Full-stable promotion task (base task specialization)**<br>
Location: NATS
```yaml
id: promote2
tasktype: node-promotion
hostId: node1
status: succeeded
concurrency:
  exclusiveResources: ["cluster:cluster1:membership", "host:node2:system"]
inputs:
  nodeId: node2
  toStatus: full-stable
  force: false
context:
  previousStatus: full
result:
  membershipStatus: full-stable
```

## node demotion

### full-stable to full
- the last full-stable member cannot be demoted
- if the demotion requires a bootstrap server, a surviving full-stable member starts it and waits for it to become healthy before continuing
- if the demoted node hosts the bootstrap server, transfer bootstrap ownership to a surviving full-stable member first
- move replicas off the node and remove its JetStream server from metadata membership before stopping it
- update replication based on the future full-stable count
- connect the node's NATS client to a surviving full-stable member and stop its embedded server
- retain cache hosting and cache peer membership
- retain the node's credential-store recipient entry and full permissions; persist full status

### full to pending
- a full-stable node first completes the server demotion above before becoming pending
- stop the binary cache and remove the node from the cache peer list
- remove the node's age key from the credential store recipients
- remove full permissions and apply pending permissions
- persist pending status; the node remains a client connected to a full-stable member

**Node demotion task (base task specialization)**<br>
Location: NATS
```yaml
id: demote1
tasktype: node-demotion
hostId: node1 # Remains a full member while node2 is demoted.
status: succeeded
concurrency:
  exclusiveResources: ["cluster:cluster1:membership", "host:node2:system"]
inputs:
  nodeId: node2
  toStatus: pending # Use full to remove server responsibilities only.
  force: false
context:
  previousStatus: full-stable
result:
  membershipStatus: pending
```

## node removal
- removal is a task coordinated by a surviving full member
- online full-stable removal follows the server demotion procedure, including bootstrap transfer, replica evacuation, and metadata peer removal
- full permissions, credential access, and cache peer membership are removed as applicable
- the last full-stable member cannot be removed without another full-stable member or cluster destruction
- normal removal contacts the node; force removal supports an offline node

**Node removal task (base task specialization)**<br>
Location: NATS
```yaml
id: remove1
tasktype: node-removal
hostId: node1 # Surviving full member, not the node being removed.
status: succeeded
concurrency:
  exclusiveResources: ["cluster:cluster1:membership", "host:node2:system"]
inputs:
  nodeId: node2
  force: false
context:
  previousStatus: full
result:
  membershipStatus: removed
```

## forced membership actions
- hosts can be forced to promote to full or forced to demote to pending; server participation changes use the full-stable transitions above
- only full members can send forceful membership actions
- force demotion cannot happen on the last full-stable member
- promotion, demotion, and removal tasks are queued one at a time across the cluster to avoid race conditions
- these actions are not a recommended path, they are here for testing and fallback scenarios.

### force removal
- hosts can also me force removed from the cluster when they are no longer online
- if a host that was force removed comes online, it will query a force removal http endpoint after it failed to connect.
- the endpoint will respond if the host was removed and the node will change its state to unjoined.
- the removal payload from the cluster will be signed by the cluster so the node can confirm its validity
- a removal can also be removed to let the node rejoin the cluster

**Force-removal task (base task specialization)**<br>
Location: NATS
```yaml
id: remove2
tasktype: node-removal
hostId: node1 # Surviving full member; node2 is offline.
status: succeeded
concurrency:
  exclusiveResources: ["cluster:cluster1:membership", "host:node2:system"]
inputs:
  nodeId: node2
  force: true
context:
  previousStatus: full-stable
result:
  membershipStatus: removed
```

**Signed force-removal response**<br>
Location: Daemon HTTP response; removal record in NATS
```yaml
payload:
  clusterId: cluster1
  nodeId: node2
  status: removed
  removedAt: "2026-10-29T18:10:00Z"
signature: "<cluster signature>"
```

## gitops
- configure one or more git repositories
- pull on cadence and notify other hosts when updates are available
- used for templates, deployments
- tracked repos can be added or removed through the interface
- credentials can be associated to a tracked repo for publishing to the repo or private repo auth

**Tracked repository without credentials**<br>
Location: NATS
```yaml
id: repo1
name: nixos-configs
url: https://github.com/example/nixos-configs.git
credentialId: null
ref: main
sha: "123412341234"
```

**Tracked repository with credentials**<br>
Location: NATS
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
- one global credential-store record contains authorized full member ids and their current public age keys, maintained from membership records
- each credential record in NATS stores the complete SOPS-encrypted file, including embedded decryption metadata, in its content field; no .sops.yaml is stored for the credential store
- membership or public-key changes update the global recipient record and all existing credential documents; changing the global record alone does not change decryption access
- only full members have access to decrypt and update the secrets.
- everytime a full member is added, their public key is added to sops and keys are updated
- members demoted to pending or removed from the cluster are removed from the recipient list; full-stable to full does not revoke access
- credential sops is initialized on cluster creation and stores the cluster CA and default nix signing key
- actions that require credentials will be performed on hosts that have access to the credentials
- EX: publishing a recipe, updating sops in for a configuration or enrolling another host

**Credential store record**<br>
Location: NATS
```yaml
clusterId: cluster1
recipients: # Current full members and public keys from membership records.
  - nodeId: node1
    agePublicKey: "<node1 age public key>"
  - nodeId: node2
    agePublicKey: "<node2 age public key>"
```

**Encrypted credential record**<br>
Location: NATS
```yaml
id: githubCredentialId
name: private-repository-access
type: git
content: |
  <complete SOPS-encrypted file, including its embedded metadata>
```

##  enrollment
- process where new hosts running nixmox join a cluster
- Enrollment establishes trust; it does not require a previously known
 cluster or node identity.
- unjoined nodes can request to join a cluster. requests need confirmation
- clusters that send enrollment requests to unjoined nodes need no confirmation.
- a decision to join happens either way giving priority to the cluster for authoritative trust.
- enrollment requests require a discovery id or endpoint address to the cluster or node
- removed nodes cannot join the cluster
- enrollment actions on the cluster side are cross host
- requests can be initiated or approved by another full member, not just the contact host communicating with the unjoined node
- the contact host must be a full member and remains the certificate issuer and HTTP contact throughout enrollment; pending members cannot add members
- the enrollment request record is shared in NATS; decisions made on another host are acted on by the contact host
- discovery-based requests retain the selected public-key thumbprint: the cluster CA key for a discovered cluster, or the node key for a discovered unjoined node

Key exchange
This is varient of a typical CSR flow
validation occurs at all steps to ensure that the initial request is still talking to the same cluster/node throughout
api routes with internal are for daemon to daemon communication, not meant for interface interaction
unjoined node flow
1. unjoined nodes will send a csr request to a cluster node with their node id, name, and public key
- unjoined interface would send POST `<unjoined>/enrollment/request` with the discovered cluster to connect to
- unjoined -(name,id,public-key,csr)-> cluster POST `<cluster>/internal/enrollment/request` which sends back request id and public CA
- if discovery was used, validate that the cluster CA public-key thumbprint matches the returned public CA
- unjoined should save and persist the requestId, public CA and contact endpoint until enrollment completes or is rejected
2. the contact host receives the request, validates the CSR, and saves the enrollment record in NATS awaiting approval or rejection.
- Status updates here from cluster: GET `<cluster>/enrollment/<request-id>`
- Unjoined serves status updates from cluster here GET `<unjoined>/enrollment/<request-id>`
- rejections are saved so further requests matching the endpoint, public key, or node id are blocked; matching any one is sufficient. A rejection can still be removed if accidental.
POST `<cluster>/enrollment/<request-id>/reject` via cluster side interface
- approval records the approving full member; the contact host issues the certificate and saves it on the request
POST `<cluster>/enrollment/<request-id>/approve` via cluster side interface
3. After approval
- the contact host adds a pending membership record into NATS
- the contact host sends the request id and signed cert back to the sender POST `<unjoined>/internal/enrollment/confirm`
- the unjoined node validates the request id, CA signature, node identity, and certificate public key against its saved request and local key
- if unjoined missed the notification then it can poll request status on `<cluster>/enrollment/<request-id>`
4. unjoined node will configure its nats client with its private key, the new issued certificate and the public CA then join the cluster.

cluster flow:
1. cluster node1 discovers unjoined nodeX and sends that to the cluster over nats
2. cluster node2 or node1 initiates an enrollment request that goes to nats to add nodeX
- interface requests POST `<node1>/enrollment/request` or POST `<node2>/enrollment/request` with nodeX discovery id and node1 as contactHost
- node1 must be a full member that observed nodeX; copy the endpoint and expected node-key thumbprint into the enrollment record
- the initiating full member authorizes this request; no separate approval is required
- and status can be found at `/enrollment/<request-id>` just like unjoined flow
3. node1 sees the request record and sends an HTTP POST `<nodeX>/internal/enrollment/add` with cluster id, public CA, contact endpoint, and request id
- nodeX responds with its node id, name, and CSR, and persists the cluster details and request id
- node1 validates the CSR and checks its node identity and public-key thumbprint against the selected discovery before issuing
4. node1 saves the CSR and issued certificate on the enrollment record, adds a pending membership record, and sends the request id and signed cert POST `<nodeX>/internal/enrollment/confirm`
- nodeX validates the request id, CA signature, node identity, and certificate public key against the details saved during /add and its local key
- if confirmation is missed, nodeX can retrieve the same result through the contact host's request status endpoint

**Enrollment request record**<br>
Location: NATS
```yaml
id: enrollment1
status: completed
createdAt: "2026-10-29T18:00:00Z"

joiningNode: # Birch is joining the cluster.
  nodeId: birchNodeID
  name: birch
  endpoint: http://birch.local:8080
  publicKeyThumbprint: "sha256:<birch public key digest>"
  csr: "<birch CSR PEM>"
  issuedCertificate: "<birch certificate PEM signed by the cluster CA>"
  discoveryId: discovery1 # Is present when initiated cluster side using a discovery id

cluster: # The cluster Birch is joining.
  id: cluster1
  caPublicKeyThumbprint: "sha256:<cluster CA public key digest>"
  caCertificate: "<public cluster CA certificate PEM>"
  natsEndpoints:
    - tls://cedar.local:4222
    - tls://oak.local:4222
  contactNode: # Cedar handles HTTP with Birch and issues its certificate.
    nodeId: cedarNodeID
    endpoint: http://cedar.local:8080

initiator: oakNodeId

decision:
  result: approved
  byNodeId: oakNodeId # this will match initiator when request is coming cluster side
  decidedAt: "2026-10-29T18:01:00Z"

```

**Enrollment request record — joining node awaiting approval**<br>
Location: NATS
```yaml
id: enrollment2
status: pending
createdAt: "2026-10-29T18:00:00Z"

joiningNode:
  nodeId: birchNodeID
  name: birch
  endpoint: http://birch.local:8080
  publicKeyThumbprint: "sha256:<birch public key digest>"
  csr: "<birch CSR PEM>"
  issuedCertificate: null
  discoveryId: null

cluster:
  id: cluster1
  caPublicKeyThumbprint: "sha256:<cluster CA public key digest>"
  caCertificate: "<public cluster CA certificate PEM>"
  natsEndpoints:
    - tls://cedar.local:4222
    - tls://oak.local:4222
  contactNode:
    nodeId: cedarNodeID
    endpoint: http://cedar.local:8080

initiator: birchNodeID

decision: null # Waiting for a full member to approve or reject.
```

**Enrollment request record — cluster add by endpoint, before CSR exchange**<br>
Location: NATS
```yaml
id: enrollment3
status: pending # Authorized, but Cedar has not contacted the endpoint yet.
createdAt: "2026-10-29T18:00:00Z"

joiningNode:
  nodeId: null # Learned from the /internal/enrollment/add response.
  name: null
  endpoint: http://birch.local:8080
  publicKeyThumbprint: null
  csr: null
  issuedCertificate: null
  discoveryId: null # Explicit endpoint, not discovery.

cluster:
  id: cluster1
  caPublicKeyThumbprint: "sha256:<cluster CA public key digest>"
  caCertificate: "<public cluster CA certificate PEM>"
  natsEndpoints:
    - tls://cedar.local:4222
    - tls://oak.local:4222
  contactNode:
    nodeId: cedarNodeID
    endpoint: http://cedar.local:8080

initiator: oakNodeId

decision:
  result: approved # Oak authorized the add; no separate approval is required.
  byNodeId: oakNodeId
  decidedAt: "2026-10-29T18:00:00Z"
```

##  discovery
- make enrollment easier by letting nodes find clusters and clusters find nodes
- broadcast happens on common port
- sends out available endpoints, node id/cluster id and name
- this is not required to add hosts, it is just to make it easier

**Cluster discovery broadcast**<br>
Location: Network discovery broadcast
```yaml
nodeId: node1 # Advertising full member; enrollment contact and issuer.
clusterId: cluster1
name: homelab
endpoints:
  - http://cedar.local:8080
identity:
  kind: cluster-ca
  publicKeyThumbprint: "sha256:<cluster CA public key digest>"
```

**Unjoined node discovery broadcast**<br>
Location: Network discovery broadcast
```yaml
nodeId: node2
clusterId: null
name: birch
endpoints:
  - http://birch.local:8080
identity:
  kind: node
  publicKeyThumbprint: "sha256:<node public key digest>"
```

**Discovery listener record**<br>
Location: NATS when the listener is clustered; in memory when host unjoined
```yaml
id: discovery1
observedByHostId: node1
lastSeenAt: "2026-10-29T18:00:00Z"
advertisement: # Received cluster or unjoined node broadcast.
  nodeId: node2
  clusterId: null
  name: birch
  endpoints:
    - http://birch.local:8080
  identity:
    kind: node
    publicKeyThumbprint: "sha256:<node public key digest>"
```

## configurations
- configurations is a registry of nixos configurations
- they can be from git repos or recipes rendered inside of a repo
- remote configurations are automatically registered from tracked repos
- they do not have to be commited to the remote repo when using a recipe
- hosts must select a configuration from the registry to become full members.

**Configuration Record**<br>
Location: NATS
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

**Recipe Record**<br>
Location: NATS
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
**Repository Recipe Definition**<br>
Location: Git repository: .nixmox/recipes/myrecipe.yaml
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

**Recipe publication task (base task specialization)**<br>
Location: NATS
```yaml
id: publish1
tasktype: recipe-publication
hostId: node1
status: succeeded
inputs:
  recipeId: recipe1-uuid
  recipeVersion: 6
  targetRepo:
    id: repo1
    ref: main
    sha: "123412341234"
  mode: pull-request
result:
  commitSha: "abcdef123456"
  pullRequestUrl: https://github.com/example/nixos-configs/pull/12
```

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
- tasks have concurrency protection setting for tasks types that would conflict. Ex: power tasks or deployments

**Base task record**<br>
Location: NATS
```yaml
id: build1
tasktype: build
hostId: node1
status: running # queued, running, manual-review, succeeded, failed, cancelled, unknown
createdAt: "2026-10-29T18:00:00Z"
startedAt: "2026-10-29T18:00:01Z"
finishedAt: null
links:
  parentTaskId: deploy1
  retryOfTaskId: null
  afterSuccessOfTaskIds: []
  childTasks: []
  outputs: [] # References such as {id: review1, kind: deployment-diff}.
concurrency:
  exclusiveResources: [] # Conflicting tasks share a resource, e.g. host:node1:system.
inputs: {} # Type-specific inputs; specialization examples omit unchanged base fields.
context: {} # Durable execution facts retained across restarts, not live progress.
review: null # When awaiting approval: {outputId: review1, approvedAt: null}.
result: null
error: null # On failure: {message: "Build failed; see task logs"}.
```

### task log adapters
- tasks can use log adapters to parse logs into task progress telemetry
- the raw log will still be streamed, the adapter will parse independently during task execution 
- an example adapter would be nix builds which can send download speed, number of packages built, progress of packages being downloaded etc.

**Build progress update**<br>
Location: NATS (live updates)
```yaml
taskId: build1
hostId: node1
observedAt: "2026-10-29T18:00:20Z"
tasktype: build
phase: building
progress:
  buildsCompleted: 4
  buildsTotal: 12
  downloadsCompleted: 8
  downloadsTotal: 10
  downloadBytesPerSecond: 10485760
```

## power tasks
- there are tasks for reboot and shutdown of a host
- current boot information is added to the task
- reboot is considered complete once back online and reboot validated using boot info
- shutdown is considered complete once the shutdown command is sent to the host. ensure shutdown command is scheduled on a small delay.
- check quorum before powering down. transfer bootstrap if running on host and ensure healthy transition before shutdown.
- power tasks do not change membership status or full-stable count
- quorum and bootstrap checks apply when stopping a full-stable host; stopping a client-only full or pending host does not remove a server
- if this is the only full-stable node skip bootstrap transfer
- force option will skip all checks and result in immediate power down.
- evacuate option will try to move data replicas off of node before power down. 

**Reboot task (base task specialization)**<br>
Location: NATS
```yaml
id: reboot1
tasktype: reboot
hostId: node1
status: succeeded
concurrency:
  exclusiveResources: ["host:node1:system"]
inputs:
  force: false
  evacuate: false
context:
  previousBootId: boot1
result:
  bootId: boot2
  validatedAt: "2026-10-29T18:05:00Z"
```

## daemon update
- daemon systemd service is not restarted on deployment apply.
- a task is available for restarting the daemon similar to power tasks
- daemon restart behaves just like reboot but instead restarts daemon and the completion check is on daemon start time.
- hosts should update their membership record in nats if they have a daemon update ready and require daemon restart

**Daemon restart task (base task specialization)**<br>
Location: NATS
```yaml
id: restart1
tasktype: daemon-restart
hostId: node1
status: succeeded
concurrency:
  exclusiveResources: ["host:node1:system"]
inputs:
  force: false
  evacuate: false
context:
  previousStartedAt: "2026-10-29T18:00:00Z"
result:
  startedAt: "2026-10-29T19:00:00Z"
  version: "0.2.0"
```

## host telemetry, progress updates and log streams
- live telemetry and logs are sent from all hosts to display in any interface connected to any host daemon
- information is sent via nats back to the daemon which forwards the information to the interface
- logs streams must keep full history when streaming batached logs using replay
- logs are stored for historical viewing and will have a ttl of 5 days by default.
- telemetry and progress updates do not need full history or replay, the most up to date information is favored here.
- host metrics can have different update frequencies
- high frequency metrics 2 sec
  - cpu usage
  - memory usage
  - disk bytes/sec
  - IOPS
  - network usage
  - network latency
- low frequency metrics 60sec
  - disk info: partitions, size, free space, format
  - memory info: mem type and freq
  - cpu info: name, arch, core count, etc
  - network info: interfaces, interface info 
- host metrics should use opentelemetry format and libraries

**Host telemetry and log records (independent records, logical view)**<br>
Location: NATS (live telemetry; retained logs)
```yaml
# OTLP-shaped YAML; metrics and logs are independent payloads.
resourceMetrics:
  - resource:
      attributes:
        - key: host.id
          value: {stringValue: node1}
    scopeMetrics:
      - scope: {name: nixmox.host}
        metrics:
          - name: system.memory.usage
            unit: By
            gauge:
              dataPoints:
                - attributes:
                    - key: system.memory.state
                      value: {stringValue: used}
                  timeUnixNano: "1793296820000000000"
                  asInt: "8589934592"
          - name: system.disk.io
            unit: By
            sum:
              aggregationTemporality: 2 # Cumulative; derive bytes/sec from successive samples.
              isMonotonic: true
              dataPoints:
                - attributes:
                    - key: device
                      value: {stringValue: nvme0n1}
                    - key: disk.io.direction
                      value: {stringValue: read}
                  startTimeUnixNano: "1793296800000000000"
                  timeUnixNano: "1793296820000000000"
                  asInt: "209715200"
resourceLogs:
  - resource:
      attributes:
        - key: host.id
          value: {stringValue: node1}
    scopeLogs:
      - scope: {name: nixmox.tasks}
        logRecords:
          - timeUnixNano: "1793296820000000000"
            severityNumber: 9
            severityText: INFO
            body: {stringValue: "building cedar system closure"}
            attributes:
              - key: nixmox.task.id
                value: {stringValue: build1}
```

## cluster cache
- a cluster cache is used to transfer built artifacts across hosts
- both full and full-stable members host cache peers; pending members can fetch artifacts through reachable cache gateways
- the cache is a gateway proxy to all other peer caches so only one url is used
- the gateway proxy uses bounded timeouts and fail over to other peers to try and prevent stalls and failures in case peers go down
- gateway proxy will try all peers to see if they have the package cached
- /cache is the gateway and /store-cache is the local cache on the host the gateway queries.
- the local cache is simple and embedded into the app to keep nixmox self contained.
- cluster cache will use the generated cache signing key if one is not configured.
- the private cache key is added as a cluster credential to distribute to all full members
- nixmox will add the the cache proxy and public key as extra substituters and trusted keys via cli flags on build actions
- the nixmox module can configure nix settings to trust the cache proxy and the keys

**Cluster cache information**<br>
Location: Local daemon (derived cache information)
```yaml
gatewayUrl: http://cedar.local:8080/cache
publicKey: "<Nix cache public signing key>"
signingCredentialId: nix-signing-key
peers:
  - nodeId: node1
    storeUrl: http://cedar.local:8080/store-cache
  - nodeId: node2
    storeUrl: http://birch.local:8080/store-cache
```

##  builds tasks
- nix build tasks are used to build nixos configurations or packages on a host
- you can schedule builds on any host from the interface allowing for cross host building
- nix builds should configure a log adapter inspired by nix output manager to send build telemetry

**Build task (base task specialization)**<br>
Location: NATS
```yaml
id: build1
tasktype: build
hostId: node1
status: succeeded
links:
  parentTaskId: deploy1
  retryOfTaskId: null
  afterSuccessOfTaskIds: []
  childTasks: []
  outputs: [] # References such as {id: review1, kind: deployment-diff}.
inputs:
  targetRepo:
    id: repo1
    ref: main
    sha: "123412341234"
  flakeDir: .
  installable: configurations.nixos.cedar.config.system.build.toplevel
result:
  storePaths:
    - /nix/store/<hash>-nixos-system-cedar
```

## deployment tasks
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

**Deployment task (base task specialization)**<br>
Location: NATS
```yaml
id: deploy1
tasktype: deployment
hostId: node1
status: manual-review
concurrency:
  exclusiveResources: ["host:node1:system"]
inputs:
  configurationId: cedar-uuid
  targetRepo:
    id: repo1
    ref: main
    sha: "123412341234"
  buildHostId: node1
  autoApprove: false
links:
  childTasks:
    - taskId: build1
      role: build
  outputs:
    - id: review1
      kind: deployment-diff
review:
  outputId: review1
  approvedAt: null
result: null # On success: {generation: 43, storePath: /nix/store/<hash>-nixos-system-cedar}.
```

### deployment diff review
- compare current generation to new generation
- save store paths of each in the review
- after approval, just activate do not build again

**Deployment review output**<br>
Location: NATS
```yaml
id: review1
taskId: deploy1
kind: deployment-diff
current:
  generation: 42
  storePath: /nix/store/<old-hash>-nixos-system-cedar
proposed:
  storePath: /nix/store/<hash>-nixos-system-cedar
packages:
  added: [{name: ripgrep, version: "14.1.1"}]
  removed: []
  updated:
    - name: curl
      fromVersion: "8.10.0"
      toVersion: "8.11.0"
```

### rollback
- rollback to the last generation can be performed on the fly
- rollback to specific generations will not be done in v1 right now
- rollback can be performed multiple times going further back as long as the generation has not been garbage collected

**Rollback task (base task specialization)**<br>
Location: NATS
```yaml
id: rollback1
tasktype: rollback
hostId: node1
status: succeeded
concurrency:
  exclusiveResources: ["host:node1:system"]
inputs:
  target: previous-generation
result:
  generation: 42
  storePath: /nix/store/<old-hash>-nixos-system-cedar
```

### fresh deployments
- requires an installer environment; entering it is a separate task
- select a configuration or recipe, then run install-finalize compatibility checks against the intended installed layout and use the best compatible result before formatting
- build the system, format the selected disks with disko, and install without a review step
- run install-finalize
- complete after validating cluster connectivity, the expected generation, and preserved identity; promote the host to full

**Fresh deployment task (base task specialization)**<br>
Location: NATS
```yaml
id: install1
tasktype: fresh-deployment
hostId: node2
status: succeeded
concurrency:
  exclusiveResources: ["host:node2:system"]
inputs:
  recipeId: recipe1-uuid
  recipeVersion: 6
  targetRepo:
    id: repo1
    ref: main
    sha: "123412341234"
  buildHostId: node1
links:
  childTasks:
    - taskId: install-build1
      role: build
context:
  previousBootId: installer-boot1
  finalizeId: finalize-pi-uboot-sd # Selected by checks for SD U-Boot with NVMe root.
result:
  bootId: installed-boot1
  generation: 1
  validation:
    clusterConnected: true
    expectedGenerationActive: true
    identityPreserved: true
  membershipStatus: full
```

## installers
- a hardware-specific boot environment containing the daemon and installer TUI; supports fresh deployments only
- check connectivity on startup and prompt for wired or Wi-Fi setup if needed
- use preserved identity when supplied; otherwise generate node and SSH host keys and a node id
- definitions contain hardware requirements and pinned build inputs; artifacts belong to build tasks
- installers can be built on another capable host

**Installer definition**<br>
Location: NATS
```yaml
id: installer-pi-uboot
name: Raspberry Pi CM4 U-Boot installer
requirements:
  systems: [aarch64-linux]
  boards: [raspberry-pi-cm4]
  firmware: uboot
build:
  targetRepo:
    id: repo1
    ref: main
    sha: "123412341234"
  flakeDir: .
  installable: packages.aarch64-linux.installer-pi-cm4
```

## install-prepare
- each implementation checks the current host hardware, boot layout, and selected installer, returning compatibility and preference; the task does not contain hardware-selection logic
- for a Pi booting entirely from NVMe, prepare-pi-eeprom can stage the SD installer and configure EEPROM boot selection for entry
- stages a built installer, preserves node identity, cluster enrollment, Wi-Fi configuration, and the launch task id, then performs the reboot or kexec transition
- keeps boot and preservation files outside disks that installation will format
- preserves LAN access to a full-stable member for NATS and artifacts without requiring overlay credentials
- owns its temporary boot state and cleanup
- for Pi SD U-Boot, stage the kernel, initrd, and device tree on SD and configure extlinux to boot the installer

supported:
- prepare-kexec
- prepare-efi
- prepare-pi-uboot-sd
- prepare-pi-eeprom

**Install-prepare definition**<br>
Location: NATS
```yaml
id: prepare-pi-eeprom
name: Prepare Raspberry Pi NVMe to SD installer entry
method: pi-eeprom # Implementation owns compatibility checks and execution.
```

**Install-prepare preservation record — host to installer**<br>
Location: Local private SD handoff into the installer, outside the installation target disks
```yaml
nodeId: node2
status: pending
environment: installer
installerId: installer-pi-uboot
system: aarch64-linux
previousBootId: boot-before-installer
resumeTaskIds: [launch1]

# File references name private files carried into the installer with this record.
identity:
  privateKeyFile: node.key
  sshHostKeyFile: ssh_host_ed25519_key
cluster:
  id: cluster1
  certificateFile: node.crt
  caFile: cluster-ca.crt
  natsEndpoints:
    - tls://192.168.1.20:4222
network:
  wifiConfigFiles: [wifi.nmconnection]

```

## install-finalize
- each implementation checks target hardware and the intended installed configuration, returning compatibility and preference
- runs after installation and before reboot; preserves identity, enrollment, Wi-Fi configuration, deployment task id, and validation information into the installed system
- configures boot from the installed configuration and target hardware
- methods are daemon implementations running in the installer environment
- for Pi SD U-Boot, keep firmware and U-Boot on SD and generate boot files for the new system and NVMe root using NixOS bootloader tooling; the installed configuration must maintain that SD boot partition
- select the installed boot entry after its files and preservation are written

supported:
- finalize-kexec
- finalize-pi-uboot-sd
- finalize-pi-eeprom
- finalize-efi

**Install-finalize definition**<br>
Location: NATS
```yaml
id: finalize-pi-uboot-sd
name: Finalize Raspberry Pi SD U-Boot with NVMe root
method: uboot-sd # Implementation owns compatibility checks and execution.
```

**Install-finalize preservation record — installer to host**<br>
Location: Local destination installation, written by install-finalize before reboot
```yaml
nodeId: node2
status: pending # Promoted to full after post-install validation.
environment: installed
previousBootId: installer-boot1
resumeTaskIds: [install1]
expectedGeneration: 1

# File references name private files copied alongside this record, not old-host paths.
identity:
  privateKeyFile: node.key
  sshHostKeyFile: ssh_host_ed25519_key
cluster:
  id: cluster1
  certificateFile: node.crt
  caFile: cluster-ca.crt
  natsEndpoints:
    - tls://192.168.1.20:4222
network:
  wifiConfigFiles: [wifi.nmconnection]
```

## launch installer task
- reject if already in an installer; otherwise select an installer and run install-prepare compatibility checks against the current host layout to choose the best compatible result
- build the installer through a child task and transfer the output to the target host if needed
- demote full/full-stable hosts to pending, completing server evacuation where required
- invoke install-prepare; complete when the host returns in the installer environment

**Launch installer task (base task specialization)**<br>
Location: NATS
```yaml
id: launch1
tasktype: launch-installer
hostId: node2
status: succeeded
concurrency:
  exclusiveResources: ["host:node2:system"]
inputs:
  installerId: installer-pi-uboot # Selected for the target board.
  buildHostId: node1
links:
  childTasks:
    - taskId: installer-build1 # Builds inputs.installerId.
      role: build
context:
  previousBootId: boot-before-installer
  prepareId: prepare-pi-eeprom # Selected by checks for the current NVMe-only boot layout.
result:
  method: pi-eeprom
  environment: installer
  bootId: installer-boot1
  membershipStatus: pending
```

### local network management
- hosts booted into an installer environment may need to connect to wifi network to talk with a cluster
- a utility is bundled into the nixmox interface to manage a network connection for the local host
- the nixmox local network utility can search for and show available wifi networks and signal strength
- a network can be selected and a password entered to connect

**Local Wi-Fi network listing**<br>
Location: Local daemon (network listing)
```yaml
hostId: node2
interface: wlan0
networks:
  - ssid: homelab
    security: wpa2
    signalPercent: 82
    connected: true
```

## boot media
- sd cards or usb devices can be formatted with a bootable installer
- in the interface, a user can select which host the drive is connected to, default is the daemon the interface is connected to
- select a drive from a dropdown
- select an installer definition for the hardware that will boot the media; it does not need to be an enrolled host
- select a host capable of building that installer, defaulting to the current host when capable
- the host writing the drive, the build host, and the hardware that will boot the media can all be different
- build the selected installer, transfer the build output to the host owning the drive if needed, and write it to the selected drive
- hosts in pending or in an installer state can not create media
- no confirmation necessary on drive selected

**Boot media task (base task specialization)**<br>
Location: NATS
```yaml
id: media1
tasktype: create-boot-media
hostId: node1 # Owns and writes the selected drive; not the installer target.
status: running
concurrency:
  exclusiveResources: ["host:node1:system"]
inputs:
  device: /dev/disk/by-id/usb-example-installer
  installerId: installer-pi-uboot # Determines the target hardware and build inputs.
  buildHostId: node3 # Builds the installer; may differ from the drive-owning host.
links:
  childTasks:
    - taskId: media-build1
      role: build
result: null
```

## Status / Health Checking
- reachability/status of other hosts should come from nats, not a custom hookup
- a /health endpoint is added to the daemon so user interfaces connected to it can check reachability to the daemon
- health endpoint is not queried by other daemons
- the health endpoint also returns nats server/client status and reachability to the server/cluster
- the local NATS server check is not applicable for full or pending members, which run only clients
- interface can inform user of where the issue lies between it and the cluster
- endpoint can also include a general daemon status: starting, online, disconnected, shutting down
- health poller should use appropriate timeout, retry, and probe logic

**Daemon health response**<br>
Location: Daemon HTTP /health response
```yaml
nodeId: node1
status: online
checkedAt: "2026-10-29T18:00:20Z"
checks:
  natsClient: {status: healthy}
  natsServer: {status: healthy}
  cluster: {status: healthy}
```

## multi-network
- discovery and enrollment use LAN; installers do not require overlay credentials
- members publish their LAN and overlay endpoints and keep them updated as interfaces change
- Nixmox prefers reachable overlay endpoints, falls back to LAN, and automatically updates NATS routes and client connection candidates; this also applies to cache and other HTTP access
- full-stable members accept NATS connections on LAN and overlay interfaces; every full-stable pair must have a direct route, which may use either network
- full and pending members need only one reachable full-stable server, with alternatives for reconnect; only full-stable members publish NATS listener endpoints
- network changes preserve node identity and membership status; TLS certificates must cover the connection names/IPs used

**Member network endpoints**<br>
Location: NATS, within the membership record
```yaml
nodeId: node1
endpoints:
  - network: overlay
    interface: tailscale0
    http: http://cedar.example.ts.net:8080
    natsClient: tls://cedar.example.ts.net:4222
    natsRoute: nats-route://cedar.example.ts.net:6222
  - network: lan
    interface: wlan0
    http: http://192.168.1.20:8080
    natsClient: tls://192.168.1.20:4222
    natsRoute: nats-route://192.168.1.20:6222
```

## NATS quorum
- NATS/JetStream servers run only on full-stable hosts; all server counts below refer to full-stable hosts
- full and pending members use NATS clients connected to full-stable members
- a bootstrap JetStream server is launched on the first full-stable node to start the configured clustered topology
- the bootstrap stays a JetStream peer until 3 full-stable nodes are healthy, then is removed
- demotion or removal that drops the full-stable count below 3 starts the bootstrap on a surviving full-stable host before the transition is finalized
- a bootstrap record is shared to establish which full-stable host launches it; prefer hosts that previously hosted it
- with 2 full-stable hosts, the cluster can tolerate losing the host that does not run the bootstrap server
- going offline does not remove a member or change its status; a sleeping client-only full member does not affect server quorum or replication counts
- removing a full-stable member requires evacuating replicas (if online) and removing its metadata peer; removing a client-only full member requires neither
- bootstrap handling should be seamless and automatic
- data replication should go as follows:
  - One full-stable host + bootstrap: R2.
  - Two full-stable hosts + bootstrap: R3.
  - Three full-stable hosts, bootstrap retired: R3.
  - Four full-stable hosts: R3.
  - Five+ full-stable hosts: R3 and R5 on critical data.
- critical data would be cluster essentials like credentials, membership related.

**Bootstrap ownership record**<br>
Location: NATS
```yaml
clusterId: cluster1
bootstrap:
  hostId: node1
  previousHostIds: [node1]
```

## NATS Disaster Recovery
- the cluster is placed in a state where quorum is lost and forceful recovery is desired to recover
- for instance: 2 full-stable members go offline in a cluster with 3 full-stable hosts, and the 2 are known to be unrecoverable
- it is desired that the remaining node take full ownership under admin oversight to recover the cluster and reinstate new hosts
- quorum disaster recovery can be enacted in this case through an interface connected to the surviving full-stable member
- it will use nats metadata rescue to set $JS.API.META.RESCUE to "quorum_needed":1
- then the unrecoverable nodes may be removed from the cluster and the bootstrap server should start up if needed.


## security posture v1
- backend systems use encrypted traffic when talking with nats.
- daemon to daemon traffic uses http but only this is only for discovery, enrollment, cache and force removal
- interfaces cannot talk directly to nats servers, all traffic must proxy through the api.
- nats tls and secrets are generated and managed internally, no user setup 
- there is a basic authorization policy for nats
    - actions for specific hosts can only be performed by that host's local daemon not remote daemons
    - pending nodes cannot add new members or remove members

(cli, tui, gui) <-(http/sse)-> daemon <-tls-> nats jetstream server
                                  |                 |
                                 http              tls 
                                  |                 |
                               daemon <-tls-> nats jetstream server

- no user or role management. access to the api is limited to network connectivity
- interfaces are not limited to the local daemon api, they can talk directly to other daemon interfaces over the network.

## NOT SUPPORTED
- backup and restore
- no automatic rollbacks
- users/auth login into interfaces 
- reinstall of the last full-stable host; another full-stable member must preserve the cluster.

## daemon components and flow
See [componenets.md](componenets.md) for components, startup flow, NATS subjects and storage choices.
