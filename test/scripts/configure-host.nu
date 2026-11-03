# Shared VM2/VM3 workflow: all host differences enter the configuration API.
use std/assert
use helpers.nu [record]

def main [run: path, inputs: path, host: string, device: string] {
    let endpoint = (open ($run | path join "phase1.json")).endpoint
    let repo = (open ($run | path join "06-repository.json"))
    let plan = (open $inputs)
    print --stderr $"+ nixmox --endpoint ($endpoint) --json recipe list"
    let recipes = (^nixmox --endpoint $endpoint --json recipe list | from json | inspect)
    let recipe = ($recipes.items | where name == "host" | first)
    assert equal $recipe.recipeSource.id $repo.id
    let create = {
        name: $plan.Name
        recipe: {id: $recipe.id, version: $recipe.version}
        targetRepo: {id: $repo.id, ref: $repo.ref, sha: $repo.sha, rootDir: $repo.rootDir}
        fields: ($plan | reject Modules | insert AuthorizedKey (open --raw ($run | path join "id_ed25519.pub") | str trim))
    }
    print --stderr $"+ nixmox --endpoint ($endpoint) --json configuration create --data ($create | to json -r)"
    let created = (^nixmox --endpoint $endpoint --json configuration create --data ($create | to json -r) | from json | inspect)
    assert (not $created.canRender)
    print --stderr $"+ nixmox --endpoint ($endpoint) --json configuration observe ($created.id) --version ($created.version)"
    let choices = (^nixmox --endpoint $endpoint --json configuration observe $created.id --version $created.version | from json | inspect)
    let select = {expectedVersion: $choices.version, fields: {TargetHost: $host, Modules: $plan.Modules}}
    print --stderr $"+ nixmox --endpoint ($endpoint) --json configuration update ($choices.id) --data ($select | to json -r)"
    let selected = (^nixmox --endpoint $endpoint --json configuration update $choices.id --data ($select | to json -r) | from json | inspect)
    print --stderr $"+ nixmox --endpoint ($endpoint) --json configuration observe ($selected.id) --version ($selected.version)"
    let observed = (^nixmox --endpoint $endpoint --json configuration observe $selected.id --version $selected.version | from json | inspect)
    assert equal $observed.collected.observations.disks.host $host
    assert equal $observed.collected.observations.hardware.host $host
    # Select only a disk returned by the daemon for this host, not SSH-injected observations.
    let disk = ($observed.collected.observations.disks.output | where device == $device | first)
    let selection = {expectedVersion: $observed.version, fields: {Disk: $disk.device}}
    print --stderr $"+ nixmox --endpoint ($endpoint) --json configuration update ($observed.id) --data ($selection | to json -r)"
    let configured = (^nixmox --endpoint $endpoint --json configuration update $observed.id --data ($selection | to json -r) | from json | inspect)
    assert $configured.canRender
    assert equal $configured.recipe $create.recipe
    print --stderr $"+ nixmox --endpoint ($endpoint) --json configuration render ($configured.id) --version ($configured.version)"
    let rendered = (^nixmox --endpoint $endpoint --json configuration render $configured.id --version $configured.version | from json | inspect)
    assert equal $rendered.flakeConfiguration $plan.Name
    assert equal ($rendered.files | length) 4
    assert ($rendered.files | all {|path| $path | str starts-with $"hosts/($plan.Name)/"})
    assert equal ($rendered.contents | get $"hosts/($plan.Name)/hardware.nix") $observed.collected.observations.hardware.output
    let settings = ($rendered.contents | get $"hosts/($plan.Name)/settings.json" | from json)
    assert equal $settings.hostName $plan.Name
    assert equal $settings.address $plan.Address
    assert equal $settings.mac $plan.MAC
    assert equal $settings.disk $disk.device
    record $run $"phase2-($plan.Name)-configuration" $configured
    record $run $"phase2-($plan.Name)-rendered" $rendered
}
