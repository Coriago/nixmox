# Phase 1 acceptance test: real CLI, GitHub, VM disk and activation.
use std/assert
use helpers.nu [guest wait-task record]

def main [
    run: path
    --repository: string = "https://github.com/Coriago/nixmox.git"
    --ref: string = "dev/e2e-phase2"
] {
    let endpoint = "http://127.0.0.1:18081"

    print --stderr "\n=== Phase 1 | Check VM1 disk capacity ===\n"

    let available = (guest $run "df --output=avail -B1 / | tail -1" | into int)
    assert ($available > 8589934592)

    print --stderr "\n=== Phase 1 | Verify unjoined startup ===\n"

    print --stderr $"+ nixmox --endpoint ($endpoint) --json health"
    let health = (
        ^nixmox --endpoint $endpoint --json health
        | from json
        | inspect
    )

    record $run "01-unjoined" $health
    assert equal $health.status "online"
    assert equal $health.checks.natsServer.status "not-applicable"
    assert equal $health.checks.cluster.status "not-applicable"
    let node = $health.nodeId
    assert equal (guest $run "test ! -e /var/lib/nixmox/cluster.json && echo unjoined") "unjoined"
    assert equal (guest $run "if command -v nixmox-e2e-testpkg; then exit 1; else echo absent; fi") "absent"

    print --stderr "\n=== Phase 1 | Create the cluster ===\n"

    print --stderr $"+ nixmox --endpoint ($endpoint) --json cluster create --name phase1"
    let cluster = (
        ^nixmox --endpoint $endpoint --json cluster create --name phase1
        | from json
        | inspect
    )

    record $run "02-cluster" $cluster

    print --stderr "\n=== Phase 1 | Verify full-stable membership and bootstrap ===\n"

    print --stderr $"+ nixmox --endpoint ($endpoint) --json host show ($node)"
    let member = (
        ^nixmox --endpoint $endpoint --json host show $node
        | from json
        | inspect
    )

    assert equal $member.status "full-stable"
    assert equal $member.environment "installed"
    assert equal $member.clusterId $cluster.id
    print --stderr $"+ nixmox --endpoint ($endpoint) --json health"
    let joined = (
        ^nixmox --endpoint $endpoint --json health
        | from json
        | inspect
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
        | inspect
    )

    record $run "03-credentials" $credentials
    assert ("cluster-ca" in $credentials.items.id)
    assert ("nix-signing-key" in $credentials.items.id)

    print --stderr "\n=== Phase 1 | Check signed cache readiness ===\n"

    print --stderr $"+ nixmox --endpoint ($endpoint) --json cache show"
    let cache = (
        ^nixmox --endpoint $endpoint --json cache show
        | from json
        | inspect
    )

    record $run "04-cache" $cache
    assert ($cache.publicKey | str contains ":")
    assert equal $cache.storePath "/store-cache"
    assert equal $cache.gatewayPath "/cache"
    assert not (($credentials.items | to json) | str contains '"content"')
    assert ((guest $run "curl --fail --silent http://127.0.0.1:8080/store-cache/nix-cache-info") | str contains "StoreDir: /nix/store")

    print --stderr "\n=== Phase 1 | Capture host telemetry ===\n"

    # timeout bounds an otherwise live SSE command; 124 is expected after samples.
    print --stderr $"+ timeout 12s nixmox --endpoint ($endpoint) --json host metrics ($node) --follow"
    let telemetry = (
        ^timeout 12s nixmox --endpoint $endpoint --json host metrics $node --follow
        | complete
        | inspect
    )

    record $run "05-telemetry-result" $telemetry
    $telemetry.stdout | save --force ($run | path join "05-telemetry.jsonl")
    assert ($telemetry.exit_code in [0 124])
    let samples = ($telemetry.stdout | lines | where {|line| $line | str starts-with "{" } | each {|line| $line | from json})
    assert (($samples | length) >= 2)
    assert ($telemetry.stdout | str contains "resourceMetrics")

    print --stderr "\n=== Phase 1 | Track the real GitHub repository ===\n"

    {name: "phase1", url: $repository, ref: $ref, rootDir: "test/repo"}
        | to json | save --force ($run | path join "repository.json")
    print --stderr $"+ nixmox --endpoint ($endpoint) --json repo add --file (($run) | path join "repository.json")"
    let repo = (
        ^nixmox --endpoint $endpoint --json repo add --file ($run | path join "repository.json")
        | from json
        | inspect
    )

    record $run "06-repository" $repo
    assert equal $repo.rootDir "test/repo"

    print --stderr "\n=== Phase 1 | Verify recipe and configuration discovery ===\n"

    print --stderr $"+ nixmox --endpoint ($endpoint) --json recipe list"
    let discovered = (
        ^nixmox --endpoint $endpoint --json recipe list
        | from json
        | inspect
    )

    assert ("testpkg" in $discovered.items.name)
    print --stderr $"+ nixmox --endpoint ($endpoint) --json configuration list"
    let configurations = (
        ^nixmox --endpoint $endpoint --json configuration list
        | from json
        | inspect
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
        | inspect
    )

    record $run "07-baseline-submitted" $deployment
    let baseline = (wait-task $endpoint $deployment.id)
    record $run "07-baseline-deployment" $baseline

    print --stderr "\n=== Phase 1 | Verify baseline activation ===\n"

    assert (($baseline.links.childTasks | length) == 1)
    assert equal (guest $run "readlink -f /run/current-system") $baseline.result.storePath
    assert equal (guest $run "hostname") "vm1"

    print --stderr "\n=== Phase 1 | Import the testpkg recipe ===\n"

    let source = {id: $repo.id, ref: $repo.ref, sha: $repo.sha, rootDir: "test/repo", path: ".nixmox/recipes/testpkg.yaml"}
    print --stderr $"+ nixmox --endpoint ($endpoint) --json recipe create --data ({name: "testpkg", recipeSource: ($source)} | to json -r)"
    let recipe = (
        ^nixmox --endpoint $endpoint --json recipe create --data ({name: "testpkg", recipeSource: $source} | to json -r)
        | from json
        | inspect
    )

    print --stderr "\n=== Phase 1 | Create and observe the recipe configuration ===\n"

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
        | inspect
    )

    print --stderr $"+ nixmox --endpoint ($endpoint) --json configuration observe ($created.id) --version ($created.version)"
    let configured = (
        ^nixmox --endpoint $endpoint --json configuration observe $created.id --version $created.version
        | from json
        | inspect
    )

    print --stderr "\n=== Phase 1 | Assign and deploy the recipe configuration ===\n"

    print --stderr $"+ nixmox --endpoint ($endpoint) host configuration ($node) --configuration ($configured.id) --version ($configured.version)"
    do --capture-errors {
        ^nixmox --endpoint $endpoint host configuration $node --configuration $configured.id --version $configured.version
    }
    print --stderr $"+ nixmox --endpoint ($endpoint) --json deploy --host ($node) --configuration ($configured.id) --version ($configured.version) --approve"
    let changed = (
        ^nixmox --endpoint $endpoint --json deploy --host $node --configuration $configured.id --version $configured.version --approve
        | from json
        | inspect
    )

    record $run "08-recipe-submitted" $changed
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
        | inspect
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
    print "PASS: phase 1"
}
