# Phase 1 acceptance test: real CLI, GitHub, VM disk and activation.
use std/assert
use helpers.nu [guest wait-task record]

def main [
    run: path
    --repository: string = "https://github.com/Coriago/nixmox.git"
    --ref: string = "dev/e2e-phase1"
] {
    let endpoint = "http://127.0.0.1:18081"
    let available = (guest $run "df --output=avail -B1 / | tail -1" | into int)
    assert ($available > 8589934592)
    let health = (^nixmox --endpoint $endpoint --json health | from json)
    record $run "01-unjoined" $health
    assert equal $health.status "online"
    assert equal $health.checks.natsServer.status "not-applicable"
    assert equal $health.checks.cluster.status "not-applicable"
    let node = $health.nodeId
    assert equal (guest $run "test ! -e /var/lib/nixmox/cluster.json && echo unjoined") "unjoined"
    assert equal (guest $run "if command -v nixmox-e2e-testpkg; then exit 1; else echo absent; fi") "absent"

    print "Creating the single-host cluster"
    let cluster = (^nixmox --endpoint $endpoint --json cluster create --name phase1 | from json)
    record $run "02-cluster" $cluster
    let member = (^nixmox --endpoint $endpoint --json host show $node | from json)
    assert equal $member.status "full-stable"
    assert equal $member.environment "installed"
    assert equal $member.clusterId $cluster.id
    let joined = (^nixmox --endpoint $endpoint --json health | from json)
    assert equal $joined.checks.natsClient.status "healthy"
    assert equal $joined.checks.natsServer.status "healthy"
    # Bootstrap must actually listen, not merely have healthy parent process status.
    assert ((guest $run "ss -lntH 'sport = :4223'") | is-not-empty)
    assert ((guest $run "ss -lntH 'sport = :6223'") | is-not-empty)

    let credentials = (^nixmox --endpoint $endpoint --json credential list | from json)
    record $run "03-credentials" $credentials
    assert ("cluster-ca" in $credentials.items.id)
    assert ("nix-signing-key" in $credentials.items.id)
    let cache = (^nixmox --endpoint $endpoint --json cache show | from json)
    record $run "04-cache" $cache
    assert ($cache.publicKey | str contains ":")
    assert equal $cache.storePath "/store-cache"
    assert equal $cache.gatewayPath "/cache"
    assert not (($credentials.items | to json) | str contains '"content"')
    assert ((guest $run "curl --fail --silent http://127.0.0.1:8080/store-cache/nix-cache-info") | str contains "StoreDir: /nix/store")

    # timeout bounds an otherwise live SSE command; 124 is expected after samples.
    let telemetry = (^timeout 12s nixmox --endpoint $endpoint --json host metrics $node --follow | complete)
    record $run "05-telemetry-result" $telemetry
    $telemetry.stdout | save --force ($run | path join "05-telemetry.jsonl")
    assert ($telemetry.exit_code in [0 124])
    let samples = ($telemetry.stdout | lines | where {|line| $line | str starts-with "{" } | each {|line| $line | from json})
    assert (($samples | length) >= 2)
    assert ($telemetry.stdout | str contains "resourceMetrics")

    print "Tracking the real GitHub repository under test/repo"
    {name: "phase1", url: $repository, ref: $ref, rootDir: "test/repo"}
        | to json | save --force ($run | path join "repository.json")
    let repo = (^nixmox --endpoint $endpoint --json repo add --file ($run | path join "repository.json") | from json)
    record $run "06-repository" $repo
    assert equal $repo.rootDir "test/repo"
    let discovered = (^nixmox --endpoint $endpoint --json recipe list | from json)
    assert ("testpkg" in $discovered.items.name)
    let configurations = (^nixmox --endpoint $endpoint --json configuration list | from json)
    let initial = ($configurations.items | where name == "vm1" | first)
    do --capture-errors { ^nixmox --endpoint $endpoint host configuration $node --configuration $initial.id --version $initial.version }
    let deployment = (^nixmox --endpoint $endpoint --json deploy --host $node --configuration $initial.id --version $initial.version --approve | from json)
    record $run "07-baseline-submitted" $deployment
    let baseline = (wait-task $endpoint $deployment.id)
    record $run "07-baseline-deployment" $baseline
    assert (($baseline.links.childTasks | length) == 1)
    assert equal (guest $run "readlink -f /run/current-system") $baseline.result.storePath
    assert equal (guest $run "hostname") "vm1"

    print "Importing and deploying the testpkg recipe"
    let source = {id: $repo.id, ref: $repo.ref, sha: $repo.sha, rootDir: "test/repo", path: ".nixmox/recipes/testpkg.yaml"}
    let recipe = (^nixmox --endpoint $endpoint --json recipe create --data ({name: "testpkg", recipeSource: $source} | to json -r) | from json)
    let inputs = {
        name: "vm1-testpkg"
        recipe: {id: $recipe.id, version: $recipe.version}
        targetRepo: {id: $repo.id, ref: $repo.ref, sha: $repo.sha, rootDir: "test/repo"}
        fields: {}
    }
    let created = (^nixmox --endpoint $endpoint --json configuration create --data ($inputs | to json -r) | from json)
    let configured = (^nixmox --endpoint $endpoint --json configuration observe $created.id --version $created.version | from json)
    do --capture-errors { ^nixmox --endpoint $endpoint host configuration $node --configuration $configured.id --version $configured.version }
    let changed = (^nixmox --endpoint $endpoint --json deploy --host $node --configuration $configured.id --version $configured.version --approve | from json)
    record $run "08-recipe-submitted" $changed
    let installed = (wait-task $endpoint $changed.id)
    record $run "08-recipe-deployment" $installed
    assert ($installed.result.generation > $baseline.result.generation)
    assert ($installed.result.storePath != $baseline.result.storePath)
    assert equal (guest $run "readlink -f /run/current-system") $installed.result.storePath
    let package = (guest $run "nixmox-e2e-testpkg")
    assert equal $package "nixmox-e2e-testpkg-v1"
    record $run "08-package" {output: $package}
    let final = (^nixmox --endpoint $endpoint --json host show $node | from json)
    assert equal $final.nodeId $node
    assert equal $final.status "full-stable"
    assert equal $final.daemon.startedAt $member.daemon.startedAt
    assert equal $final.configurationId $configured.id
    record $run "09-final-member" $final
    print "PASS: phase 1"
}
