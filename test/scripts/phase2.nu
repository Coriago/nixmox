# First phase-2 acceptance slice: preserve VM1, boot VM2, then discover/enroll.
# Remaining install/promotion/VM3/kexec steps are still in test/plan.md.
use std/assert
use helpers.nu [guest record]

def main [run: path, --handoff-only] {
    print --stderr "\n=== Phase 2 | Verify the live phase-1 handoff ===\n"

    let previous = (open ($run | path join "phase1.json"))
    let endpoint = $previous.endpoint
    assert equal (open --raw ($run | path join "session.pid") | str trim) $previous.vmSession
    assert equal (guest $run "cat /proc/sys/kernel/random/boot_id") $previous.bootId
    assert equal (guest $run "nixmox-e2e-testpkg") "nixmox-e2e-testpkg-v1"
    print --stderr $"+ nixmox --endpoint ($endpoint) --json cluster show"
    let cluster = (
        ^nixmox --endpoint $endpoint --json cluster show
        | from json
        | inspect
    )

    assert equal $cluster.id $previous.clusterId
    print --stderr $"+ nixmox --endpoint ($endpoint) --json host show ($previous.nodeId)"
    let member = (
        ^nixmox --endpoint $endpoint --json host show $previous.nodeId
        | from json
        | inspect
    )

    assert equal $member.daemon.startedAt $previous.daemonStartedAt
    assert equal $member.status "full-stable"
    record $run "phase2-handoff" $member
    if $handoff_only {
        return
    }

    print --stderr "\n=== Phase 2 | Verify the unjoined installer ===\n"

    let root = ($env.FILE_PWD | path join "../.." | path expand)
    let machine = (open ($root | path join "test/vms.json")).machines.vm2
    let installer = $"http://127.0.0.1:($machine.httpPort)"
    print --stderr $"+ nixmox --endpoint ($installer) --json health"
    let health = (
        ^nixmox --endpoint $installer --json health
        | from json
        | inspect
    )

    assert ($health.nodeId != $previous.nodeId)
    assert equal $health.checks.cluster.status "not-applicable"
    assert equal $health.checks.natsServer.status "not-applicable"

    print --stderr "\n=== Phase 2 | Verify the private target disk ===\n"

    let disks = (guest $run "lsblk --json --bytes --output PATH,SERIAL,TYPE,SIZE" --port 22222 | from json)
    let target = ($disks.blockdevices | where serial == $machine.diskSerial | first)
    assert equal $target.type "disk"
    assert ($target.size >= 17179869184)
    let targetDevice = $"/dev/disk/by-id/virtio-($machine.diskSerial)"
    assert equal (guest $run $"readlink -f ($targetDevice)" --port $machine.sshPort) $target.path

    print --stderr "\n=== Phase 2 | Check guest-LAN connectivity in both directions ===\n"

    # Check the private guest LAN in both directions before asking for discovery.
    let from_vm2 = (guest $run "curl --fail --silent http://192.168.77.1:8080/health" --port 22222 | from json)
    let interfaces = (guest $run "ip -j -4 address show" --port $machine.sshPort | from json)
    let lan = ($interfaces | where address == $machine.mac | first)
    let address = ($lan.addr_info | where scope == "global" | first | get local)
    let contact = $"http://($address):8080"
    let from_vm1 = (guest $run $"curl --fail --silent ($contact)/health" | from json)
    assert equal $from_vm2.nodeId $previous.nodeId
    assert equal $from_vm1.nodeId $health.nodeId
    record $run "phase2-installer" {health: $health, disk: $target}

    print --stderr "\n=== Phase 2 | Discover the installer ===\n"

    print --stderr $"+ nixmox --endpoint ($endpoint) --json discovery list"
    let discovery = (
        ^nixmox --endpoint $endpoint --json discovery list
        | from json
        | inspect
    )

    let candidate = ($discovery.items | where advertisement.nodeId == $health.nodeId | first)
    assert equal $candidate.advertisement.clusterId null
    assert ($contact in $candidate.advertisement.endpoints)

    print --stderr "\n=== Phase 2 | Enroll the discovered installer ===\n"

    # Use the discovered installer, not a manual-address enrollment shortcut.
    print --stderr $"+ nixmox --endpoint ($endpoint) --json enrollment add --discovery ($candidate.id) --contact ($previous.nodeId)"
    let request = (
        ^nixmox --endpoint $endpoint --json enrollment add --discovery $candidate.id --contact $previous.nodeId
        | from json
        | inspect
    )

    record $run "phase2-enrollment" $request

    print --stderr "\n=== Phase 2 | Verify pending installer membership ===\n"

    print --stderr $"+ nixmox --endpoint ($endpoint) --json host show ($health.nodeId)"
    let pending = (
        ^nixmox --endpoint $endpoint --json host show $health.nodeId
        | from json
        | inspect
    )

    assert equal $pending.status "pending"
    assert equal $pending.environment "installer"
    record $run "phase2-pending" $pending

    print --stderr "\n=== Phase 2 | Configure the host through the recipe workflow ===\n"

    do --capture-errors {
        ^nu ($root | path join "test/scripts/configure-host.nu") $run ($root | path join "test/configurations/vm2.json") $health.nodeId $targetDevice
    }

    print --stderr "\n=== Phase 2 | STOP: remaining installation and VM3 steps are unimplemented ===\n"

    error make {msg: "Phase 2 configuration slice complete; fresh installation, VM3 and kexec acceptance steps are not implemented yet"}
}
