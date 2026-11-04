# VM3 request-based enrollment, quorum transitions and reinstallation.
use std/assert
use helpers.nu [guest record checkpoint remaining-steps]

export const PHASE3_CHECKPOINTS = {
    "vm3-started": ["phase3-unjoined.json" "telemetry-vm3.json"]
}

def verify-handoff [run: path] {
    let previous = (open ($run | path join "phase2.json"))
    let machine = (open ($run | path join "vms.json")).machines.vm2
    assert equal (guest $run "cat /proc/sys/kernel/random/boot_id" --port $machine.sshPort) $previous.bootId
    print --stderr $"+ nixmox --endpoint ($previous.endpoint) --json host show ($previous.nodeId)"
    let member = (^nixmox --endpoint $previous.endpoint --json host show $previous.nodeId | from json)
    assert equal $member.status "full-stable"
    assert equal $member.clusterId $previous.clusterId
}

def vm3-started [run: path] {
    print --stderr "\n=== Phase 3 | Verify the normal unjoined VM3 ===\n"
    let machine = (open ($run | path join "vms.json")).machines.vm3
    let endpoint = $"http://127.0.0.1:($machine.httpPort)"
    print --stderr $"+ nixmox --endpoint ($endpoint) --json health"
    let health = (^nixmox --endpoint $endpoint --json health | from json | tee { to json --indent 2 | print --stderr })
    for check in [cluster natsClient natsServer] { assert equal ($health.checks | get $check).status "not-applicable" }
    for file in [phase1.json phase2.json] { assert ($health.nodeId != (open ($run | path join $file)).nodeId) }
    assert ((guest $run "ss -lntH '( sport = :4222 or sport = :4223 )'" --port $machine.sshPort) | is-empty)
    let disks = (guest $run "lsblk --json --bytes --output PATH,SERIAL,TYPE,SIZE,MOUNTPOINTS" --port $machine.sshPort | from json)
    let targets = ($disks.blockdevices | where serial == $machine.diskSerial)
    assert equal ($targets | length) 1
    let disk = ($targets | first)
    assert equal $disk.type "disk"
    assert ($disk.size >= 17179869184)
    assert ($disk.mountpoints | compact | is-empty)
    let targetDevice = $"/dev/disk/by-id/virtio-($machine.diskSerial)"
    assert equal (guest $run $"readlink -f ($targetDevice)" --port $machine.sshPort) $disk.path
    let contact = (guest $run "curl --fail --silent http://192.168.77.1:8080/health" --port $machine.sshPort | from json)
    assert equal $contact.nodeId (open ($run | path join "phase1.json")).nodeId
    record $run "phase3-unjoined" {health: $health, endpoint: $endpoint, targetDevice: $targetDevice, disk: $disk}
}

def main [run: path, --after: string = "", --handoff-only] {
    let pending = (remaining-steps $run $PHASE3_CHECKPOINTS $after)
    verify-handoff $run
    if $handoff_only { return }
    for step in $pending {
        match $step {
            "vm3-started" => { vm3-started $run }
            _ => { error make {msg: $"Checkpoint has no implemented step: ($step)"} }
        }
        checkpoint $run 3 $step
    }
    error make {msg: "Phase 3 request-based enrollment/approval, bootstrap retirement, demotion and kexec acceptance remain unimplemented; VM3 is retained only with --keep-vms"}
}
