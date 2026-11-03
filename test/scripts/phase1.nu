# Real CLI/GitHub acceptance; checkpoints do not decide whether replay is safe.
use std/assert
use helpers.nu [guest wait-task record checkpoint remaining-steps]

export const PHASE1_CHECKPOINTS = {
    "vm1-started": ["01-unjoined.json","telemetry-vm1.json"]
    "cluster-created": ["02-cluster.json"]
    "cluster-ready": ["02-member.json","03-credentials.json","04-cache.json"]
    "host-metrics-verified": ["05-telemetry-result.json"]
    "repository-added": ["06-repository.json"]
    "baseline-submitted": ["07-baseline-submitted.json"]
    "baseline-deployed": ["07-baseline-deployment.json"]
    "recipe-created": ["08-recipe.json"]
    "configuration-created": ["08-configuration-created.json"]
    "configuration-observed": ["08-configuration-observed.json"]
    "recipe-deployment-submitted": ["08-recipe-submitted.json"]
    "phase1-complete": ["08-recipe-deployment.json","09-final-member.json","phase1.json"]
}

def vm1-started [run: path, endpoint: string] {
    print --stderr "\n=== Phase 1 | Check VM1 disk capacity ===\n"

    let available = (guest $run "df --output=avail -B1 / | tail -1" | into int)
    assert ($available > 8589934592)

    print --stderr "\n=== Phase 1 | Verify unjoined startup ===\n"

    print --stderr $"+ nixmox --endpoint ($endpoint) --json health"
    let health = (
        ^nixmox --endpoint $endpoint --json health
        | from json
        | tee { to json --indent 2 | print --stderr }
    )

    record $run "01-unjoined" $health
    assert equal $health.status "online"
    assert equal $health.checks.natsServer.status "not-applicable"
    assert equal $health.checks.cluster.status "not-applicable"
    assert equal (guest $run "test ! -e /var/lib/nixmox/cluster.json && echo unjoined") "unjoined"
    assert equal (guest $run "if command -v nixmox-e2e-testpkg; then exit 1; else echo absent; fi") "absent"
}

def cluster-created [run: path, endpoint: string] {
    print --stderr "\n=== Phase 1 | Create the cluster ===\n"

    print --stderr $"+ nixmox --endpoint ($endpoint) --json cluster create --name phase1"
    let cluster = (
        ^nixmox --endpoint $endpoint --json cluster create --name phase1
        | from json
        | tee { to json --indent 2 | print --stderr }
    )

    record $run "02-cluster" $cluster
}

def cluster-ready [run: path, endpoint: string] {
    let node = (open ($run | path join "01-unjoined.json")).nodeId
    let cluster = (open ($run | path join "02-cluster.json"))
    print --stderr "\n=== Phase 1 | Verify full-stable membership and bootstrap ===\n"

    print --stderr $"+ nixmox --endpoint ($endpoint) --json host show ($node)"
    let member = (
        ^nixmox --endpoint $endpoint --json host show $node
        | from json
        | tee { to json --indent 2 | print --stderr }
    )

    assert equal $member.status "full-stable"
    assert equal $member.environment "installed"
    assert equal $member.clusterId $cluster.id
    print --stderr $"+ nixmox --endpoint ($endpoint) --json health"
    let joined = (
        ^nixmox --endpoint $endpoint --json health
        | from json
        | tee { to json --indent 2 | print --stderr }
    )

    assert equal $joined.checks.natsClient.status "healthy"
    assert equal $joined.checks.natsServer.status "healthy"
    # Bootstrap must actually listen, not merely have healthy parent process status.
    assert ((guest $run "ss -lntH 'sport = :4223'") | is-not-empty)
    assert ((guest $run "ss -lntH 'sport = :6223'") | is-not-empty)

    print --stderr "\n=== Phase 1 | Check credential metadata ===\n"

    print --stderr $"+ nixmox --endpoint ($endpoint) --json credential list"
    let credentials = (
        ^nixmox --endpoint $endpoint --json credential list
        | from json
        | tee { to json --indent 2 | print --stderr }
    )

    record $run "03-credentials" $credentials
    assert ("cluster-ca" in $credentials.items.id)
    assert ("nix-signing-key" in $credentials.items.id)

    print --stderr "\n=== Phase 1 | Check signed cache readiness ===\n"

    print --stderr $"+ nixmox --endpoint ($endpoint) --json cache show"
    let cache = (
        ^nixmox --endpoint $endpoint --json cache show
        | from json
        | tee { to json --indent 2 | print --stderr }
    )

    record $run "04-cache" $cache
    assert ($cache.publicKey | str contains ":")
    assert equal $cache.storePath "/store-cache"
    assert equal $cache.gatewayPath "/cache"
    assert not (($credentials.items | to json) | str contains '"content"')
    assert ((guest $run "curl --fail --silent http://127.0.0.1:8080/store-cache/nix-cache-info") | str contains "StoreDir: /nix/store")

    record $run "02-member" $member
}

def host-metrics-verified [run: path, endpoint: string] {
    let node = (open ($run | path join "01-unjoined.json")).nodeId
    print --stderr "\n=== Phase 1 | Capture host telemetry ===\n"

    # timeout bounds an otherwise live SSE command; 124 is expected after samples.
    print --stderr $"+ timeout 12s nixmox --endpoint ($endpoint) --json host metrics ($node) --follow"
    let telemetry = (
        ^timeout 12s nixmox --endpoint $endpoint --json host metrics $node --follow
        | complete
    )

    record $run "05-telemetry-result" $telemetry
    $telemetry.stdout | save --force ($run | path join "05-telemetry.jsonl")
    assert ($telemetry.exit_code in [0 124])
    let samples = ($telemetry.stdout | lines | where {|line| $line | str starts-with "{" } | each {|line| $line | from json})
    if ($telemetry.stderr | is-not-empty) { print --stderr $telemetry.stderr }
    print --stderr $"Received ($samples | length) telemetry samples; showing the latest \(all samples saved in 05-telemetry.jsonl\)."
    if ($samples | is-not-empty) { $samples | last | to json --indent 2 | print --stderr }
    assert (($samples | length) >= 2)
    assert ($telemetry.stdout | str contains "resourceMetrics")
}

def repository-added [run: path, endpoint: string, repository: string, ref: string] {
    print --stderr "\n=== Phase 1 | Track the real GitHub repository ===\n"

    {name: "phase1", url: $repository, ref: $ref, rootDir: "test/repo"}
        | to json | save --force ($run | path join "repository.json")
    print --stderr $"+ nixmox --endpoint ($endpoint) --json repo add --file (($run) | path join "repository.json")"
    let repo = (
        ^nixmox --endpoint $endpoint --json repo add --file ($run | path join "repository.json")
        | from json
        | tee { to json --indent 2 | print --stderr }
    )

    record $run "06-repository" $repo
    assert equal $repo.rootDir "test/repo"
}

def baseline-submitted [run: path, endpoint: string] {
    let node = (open ($run | path join "01-unjoined.json")).nodeId
    print --stderr "\n=== Phase 1 | Verify recipe and configuration discovery ===\n"

    print --stderr $"+ nixmox --endpoint ($endpoint) --json recipe list"
    let discovered = (
        ^nixmox --endpoint $endpoint --json recipe list
        | from json
        | tee { to json --indent 2 | print --stderr }
    )

    assert ("testpkg" in $discovered.items.name)
    print --stderr $"+ nixmox --endpoint ($endpoint) --json configuration list"
    let configurations = (
        ^nixmox --endpoint $endpoint --json configuration list
        | from json
        | tee { to json --indent 2 | print --stderr }
    )

    let initial = ($configurations.items | where name == "vm1" | first)

    print --stderr "\n=== Phase 1 | Assign and deploy the baseline configuration ===\n"

    print --stderr $"+ nixmox --endpoint ($endpoint) host configuration ($node) --configuration ($initial.id) --version ($initial.version)"
    do --capture-errors {
        ^nixmox --endpoint $endpoint host configuration $node --configuration $initial.id --version $initial.version
    }
    print --stderr $"+ nixmox --endpoint ($endpoint) --json deploy --host ($node) --configuration ($initial.id) --version ($initial.version) --approve"
    let deployment = (
        ^nixmox --endpoint $endpoint --json deploy --host $node --configuration $initial.id --version $initial.version --approve
        | from json
        | tee { to json --indent 2 | print --stderr }
    )

    record $run "07-baseline-submitted" $deployment
}

def baseline-deployed [run: path, endpoint: string] {
    let deployment = (open ($run | path join "07-baseline-submitted.json"))
    let baseline = (wait-task $endpoint $deployment.id)
    record $run "07-baseline-deployment" $baseline

    print --stderr "\n=== Phase 1 | Verify baseline activation ===\n"

    assert (($baseline.links.childTasks | length) == 1)
    assert equal (guest $run "readlink -f /run/current-system") $baseline.result.storePath
    assert equal (guest $run "hostname") "vm1"
}

def recipe-created [run: path, endpoint: string] {
    let repo = (open ($run | path join "06-repository.json"))
    print --stderr "\n=== Phase 1 | Import the testpkg recipe ===\n"

    let source = {id: $repo.id, ref: $repo.ref, sha: $repo.sha, rootDir: "test/repo", path: ".nixmox/recipes/testpkg.yaml"}
    print --stderr $"+ nixmox --endpoint ($endpoint) --json recipe create --data ({name: "testpkg", recipeSource: ($source)} | to json -r)"
    let recipe = (
        ^nixmox --endpoint $endpoint --json recipe create --data ({name: "testpkg", recipeSource: $source} | to json -r)
        | from json
        | tee { to json --indent 2 | print --stderr }
    )

    record $run "08-recipe" $recipe
}

def configuration-created [run: path, endpoint: string] {
    let repo = (open ($run | path join "06-repository.json"))
    let recipe = (open ($run | path join "08-recipe.json"))
    print --stderr "\n=== Phase 1 | Create the recipe configuration ===\n"

    let inputs = {
        name: "vm1-testpkg"
        recipe: {id: $recipe.id, version: $recipe.version}
        targetRepo: {id: $repo.id, ref: $repo.ref, sha: $repo.sha, rootDir: "test/repo"}
        fields: {}
    }
    print --stderr $"+ nixmox --endpoint ($endpoint) --json configuration create --data (($inputs) | to json -r)"
    let created = (
        ^nixmox --endpoint $endpoint --json configuration create --data ($inputs | to json -r)
        | from json
        | tee { to json --indent 2 | print --stderr }
    )

    record $run "08-configuration-created" $created
}

def configuration-observed [run: path, endpoint: string] {
    let created = (open ($run | path join "08-configuration-created.json"))
    print --stderr "\n=== Phase 1 | Observe the recipe configuration ===\n"

    print --stderr $"+ nixmox --endpoint ($endpoint) --json configuration observe ($created.id) --version ($created.version)"
    let configured = (
        ^nixmox --endpoint $endpoint --json configuration observe $created.id --version $created.version
        | from json
        | tee { to json --indent 2 | print --stderr }
    )

    record $run "08-configuration-observed" $configured
}

def recipe-deployment-submitted [run: path, endpoint: string] {
    let node = (open ($run | path join "01-unjoined.json")).nodeId
    let configured = (open ($run | path join "08-configuration-observed.json"))
    print --stderr "\n=== Phase 1 | Assign and deploy the recipe configuration ===\n"

    print --stderr $"+ nixmox --endpoint ($endpoint) host configuration ($node) --configuration ($configured.id) --version ($configured.version)"
    do --capture-errors {
        ^nixmox --endpoint $endpoint host configuration $node --configuration $configured.id --version $configured.version
    }
    print --stderr $"+ nixmox --endpoint ($endpoint) --json deploy --host ($node) --configuration ($configured.id) --version ($configured.version) --approve"
    let changed = (
        ^nixmox --endpoint $endpoint --json deploy --host $node --configuration $configured.id --version $configured.version --approve
        | from json
        | tee { to json --indent 2 | print --stderr }
    )

    record $run "08-recipe-submitted" $changed
}

def phase1-complete [run: path, endpoint: string] {
    let node = (open ($run | path join "01-unjoined.json")).nodeId
    let changed = (open ($run | path join "08-recipe-submitted.json"))
    let baseline = (open ($run | path join "07-baseline-deployment.json"))
    let member = (open ($run | path join "02-member.json"))
    let configured = (open ($run | path join "08-configuration-observed.json"))
    let cluster = (open ($run | path join "02-cluster.json"))
    let installed = (wait-task $endpoint $changed.id)
    record $run "08-recipe-deployment" $installed

    print --stderr "\n=== Phase 1 | Verify generation, package and daemon identity ===\n"

    assert ($installed.result.generation > $baseline.result.generation)
    assert ($installed.result.storePath != $baseline.result.storePath)
    assert equal (guest $run "readlink -f /run/current-system") $installed.result.storePath
    let package = (guest $run "nixmox-e2e-testpkg")
    assert equal $package "nixmox-e2e-testpkg-v1"
    record $run "08-package" {output: $package}
    print --stderr $"+ nixmox --endpoint ($endpoint) --json host show ($node)"
    let final = (
        ^nixmox --endpoint $endpoint --json host show $node
        | from json
        | tee { to json --indent 2 | print --stderr }
    )

    assert equal $final.nodeId $node
    assert equal $final.status "full-stable"
    assert equal $final.daemon.startedAt $member.daemon.startedAt
    assert equal $final.configurationId $configured.id
    record $run "09-final-member" $final

    print --stderr "\n=== Phase 1 | Save the live VM1 handoff ===\n"

    record $run "phase1" {
        endpoint: $endpoint
        nodeId: $node
        clusterId: $cluster.id
        daemonStartedAt: $final.daemon.startedAt
        bootId: (guest $run "cat /proc/sys/kernel/random/boot_id")
        vmSession: (open --raw ($run | path join "session.pid") | str trim)
    }
}

def main [
    run: path
    --after: string = ""
    --repository: string = "https://github.com/Coriago/nixmox.git"
    --ref: string = "dev/e2e-phase2"
] {
    let pending = (remaining-steps $run $PHASE1_CHECKPOINTS $after)
    let machine = (open ($run | path join "vms.json")).machines.vm1
    let endpoint = $"http://127.0.0.1:($machine.httpPort)"
    for step in $pending {
        match $step {
            "vm1-started" => { vm1-started $run $endpoint }
            "cluster-created" => { cluster-created $run $endpoint }
            "cluster-ready" => { cluster-ready $run $endpoint }
            "host-metrics-verified" => { host-metrics-verified $run $endpoint }
            "repository-added" => { repository-added $run $endpoint $repository $ref }
            "baseline-submitted" => { baseline-submitted $run $endpoint }
            "baseline-deployed" => { baseline-deployed $run $endpoint }
            "recipe-created" => { recipe-created $run $endpoint }
            "configuration-created" => { configuration-created $run $endpoint }
            "configuration-observed" => { configuration-observed $run $endpoint }
            "recipe-deployment-submitted" => { recipe-deployment-submitted $run $endpoint }
            "phase1-complete" => { phase1-complete $run $endpoint }
            _ => { error make {msg: $"Checkpoint has no implemented step: ($step)"} }
        }
        checkpoint $run 1 $step
    }
    open ($run | path join "phase1.json") | ignore
    print "PASS: phase 1"
}
