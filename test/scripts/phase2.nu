# First phase-2 acceptance slice: preserve VM1, boot VM2, then discover/enroll.
# Remaining install/promotion/VM3/kexec steps are still in test/plan.md.
use std/assert
use helpers.nu [guest record]

def main [run: path] {
    let previous = (open ($run | path join "phase1.json"))
    let endpoint = $previous.endpoint
    assert equal (open --raw ($run | path join "session.pid") | str trim) $previous.vmSession
    assert equal (guest $run "cat /proc/sys/kernel/random/boot_id") $previous.bootId
    assert equal (guest $run "nixmox-e2e-testpkg") "nixmox-e2e-testpkg-v1"
    print --stderr $"+ nixmox --endpoint ($endpoint) --json cluster show"
    let cluster = (^nixmox --endpoint $endpoint --json cluster show | from json | inspect)
    assert equal $cluster.id $previous.clusterId
    print --stderr $"+ nixmox --endpoint ($endpoint) --json host show ($previous.nodeId)"
    let member = (^nixmox --endpoint $endpoint --json host show $previous.nodeId | from json | inspect)
    assert equal $member.daemon.startedAt $previous.daemonStartedAt
    assert equal $member.status "full-stable"
    record $run "phase2-handoff" $member

    let installer = "http://127.0.0.1:18082"
    print --stderr $"+ nixmox --endpoint ($installer) --json health"
    let health = (^nixmox --endpoint $installer --json health | from json | inspect)
    assert ($health.nodeId != $previous.nodeId)
    assert equal $health.checks.cluster.status "not-applicable"
    assert equal $health.checks.natsServer.status "not-applicable"
    let disks = (guest $run "lsblk --json --bytes --output PATH,SERIAL,TYPE,SIZE" --port 22222 | from json)
    let target = ($disks.blockdevices | where serial == "nixmox-vm2" | first)
    assert equal $target.type "disk"
    assert ($target.size >= 17179869184)
    # Check the private guest LAN in both directions before asking for discovery.
    let from_vm2 = (guest $run "curl --fail --silent http://192.168.77.1:8080/health" --port 22222 | from json)
    let from_vm1 = (guest $run "curl --fail --silent http://192.168.77.2:8080/health" | from json)
    assert equal $from_vm2.nodeId $previous.nodeId
    assert equal $from_vm1.nodeId $health.nodeId
    record $run "phase2-installer" {health: $health, disk: $target}

    print --stderr $"+ nixmox --endpoint ($endpoint) --json discovery list"
    let discovery = (^nixmox --endpoint $endpoint --json discovery list | from json | inspect)
    assert ($discovery.items | is-not-empty)
    # Enrollment must use the real service; no manual certificates or NATS edits.
    print --stderr $"+ nixmox --endpoint ($endpoint) --json enrollment add --address http://192.168.77.2:8080 --contact ($previous.nodeId)"
    let request = (^nixmox --endpoint $endpoint --json enrollment add --address http://192.168.77.2:8080 --contact $previous.nodeId | from json | inspect)
    record $run "phase2-enrollment" $request
    print --stderr $"+ nixmox --endpoint ($endpoint) --json host show ($health.nodeId)"
    let pending = (^nixmox --endpoint $endpoint --json host show $health.nodeId | from json | inspect)
    assert equal $pending.status "pending"
    assert equal $pending.environment "installer"
    record $run "phase2-pending" $pending
    error make {msg: "Phase 2 enrollment slice complete; fresh installation, VM3 and kexec acceptance steps are not implemented yet"}
}
