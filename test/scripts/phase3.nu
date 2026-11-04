# VM3 request-based enrollment, quorum transitions and reinstallation.
use std/assert
use helpers.nu [guest record checkpoint remaining-steps wait-task]

export const PHASE3_CHECKPOINTS = {
    "vm3-started": ["phase3-unjoined.json" "telemetry-vm3.json"]
    "vm3-enrollment-requested": ["phase3-requested.json"]
    "vm3-enrolled": ["phase3-enrollment.json"]
    "vm3-full-submitted": ["phase3-full-submitted.json"]
    "vm3-full": ["phase3-full.json"]
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

def vm3-enrollment-requested [run: path] {
    print --stderr "\n=== Phase 3 | Request enrollment from the unjoined VM3 ===\n"
    let node = (open ($run | path join "phase3-unjoined.json"))
    let phase1 = (open ($run | path join "phase1.json"))
    let machine = (open ($run | path join "vms.json")).machines.vm3
    print --stderr $"+ nixmox --endpoint ($node.endpoint) --json enrollment request --address http://192.168.77.1:8080"
    let request = (^nixmox --endpoint $node.endpoint --json enrollment request --address http://192.168.77.1:8080 | from json | tee { to json --indent 2 | print --stderr })
    assert equal $request.status "pending"
    assert ($request.decision == null)
    assert equal $request.initiator $node.health.nodeId
    assert equal $request.joiningNode.environment "installed"
    assert equal $request.cluster.contactNode.nodeId $phase1.nodeId
    print --stderr $"+ nixmox --endpoint ($phase1.endpoint) --json host list"
    let members = (^nixmox --endpoint $phase1.endpoint --json host list | from json)
    assert ($members.items | where nodeId == $node.health.nodeId | is-empty)
    assert equal (guest $run "stat -c %a /var/lib/nixmox/enrollment.json" --port $machine.sshPort) "600"
    let hashes = (guest $run "sha256sum /var/lib/nixmox/node.key /etc/ssh/ssh_host_ed25519_key /var/lib/nixmox/age.key | cut -d ' ' -f1" --port $machine.sshPort)
    record $run "phase3-requested" {request: $request, identityHashes: $hashes}
}

def vm3-enrolled [run: path] {
    print --stderr "\n=== Phase 3 | Approve on VM2 while VM1 remains the contact ===\n"
    let requested = (open ($run | path join "phase3-requested.json"))
    let previous = (open ($run | path join "phase2.json"))
    let node = (open ($run | path join "phase3-unjoined.json"))
    let machines = (open ($run | path join "vms.json")).machines
    let approver = $"http://127.0.0.1:($machines.vm2.httpPort)"
    print --stderr $"+ nixmox --endpoint ($approver) --json enrollment approve ($requested.request.id)"
    let decision = (^nixmox --endpoint $approver --json enrollment approve $requested.request.id | from json)
    assert equal $decision.decision.byNodeId $previous.nodeId
    assert equal $decision.cluster.contactNode.nodeId $requested.request.cluster.contactNode.nodeId
    mut completed = false
    for attempt in 0..59 {
        print --stderr $"+ nixmox --endpoint ($node.endpoint) --json enrollment show ($requested.request.id)"
        let status = (^nixmox --endpoint $node.endpoint --json enrollment show $requested.request.id | from json)
        if $status.status in [failed rejected] { error make {msg: ($status | to json)} }
        if $status.status == "completed" { $completed = true; break }
        sleep 2sec
    }
    assert $completed "Requested enrollment did not complete"
    print --stderr $"+ nixmox --endpoint ($previous.endpoint) --json host show ($node.health.nodeId)"
    let member = (^nixmox --endpoint $previous.endpoint --json host show $node.health.nodeId | from json)
    assert equal $member.status "pending"
    assert equal $member.environment "installed"
    assert equal $member.clusterId $previous.clusterId
    assert equal $member.enrollmentRequestId $requested.request.id
    assert equal (guest $run "sha256sum /var/lib/nixmox/node.key /etc/ssh/ssh_host_ed25519_key /var/lib/nixmox/age.key | cut -d ' ' -f1" --port $machines.vm3.sshPort) $requested.identityHashes
    print --stderr $"+ nixmox --endpoint ($node.endpoint) --json health"
    let health = (^nixmox --endpoint $node.endpoint --json health | from json)
    assert equal $health.checks.natsClient.status "healthy"
    assert equal $health.checks.natsServer.status "not-applicable"
    record $run "phase3-enrollment" {member: $member, health: $health, decision: $decision}
}

def vm3-full-submitted [run: path] {
    print --stderr "\n=== Phase 3 | Explicitly force the installed pending node to full ===\n"
    let previous = (open ($run | path join "phase2.json"))
    let node = (open ($run | path join "phase3-unjoined.json"))
    print --stderr $"+ nixmox --endpoint ($previous.endpoint) --json host promote ($node.health.nodeId) --to full --force"
    let submitted = (^nixmox --endpoint $previous.endpoint --json host promote $node.health.nodeId --to full --force | from json)
    record $run "phase3-full-submitted" $submitted
}

def vm3-full [run: path] {
    let previous = (open ($run | path join "phase2.json"))
    let node = (open ($run | path join "phase3-unjoined.json"))
    let submitted = (open ($run | path join "phase3-full-submitted.json"))
    let promotion = (wait-task $previous.endpoint $submitted.id)
    assert equal $promotion.result.membershipStatus "full"
    print --stderr $"+ nixmox --endpoint ($previous.endpoint) --json host show ($node.health.nodeId)"
    let member = (^nixmox --endpoint $previous.endpoint --json host show $node.health.nodeId | from json)
    assert equal $member.status "full"
    print --stderr $"+ nixmox --endpoint ($node.endpoint) --json cache show"
    let cache = (^nixmox --endpoint $node.endpoint --json cache show | from json)
    assert equal $cache.publicKey (open ($run | path join "04-cache.json")).publicKey
    record $run "phase3-full" {member: $member, promotion: $promotion, cache: $cache}
}

def main [run: path, --after: string = "", --handoff-only] {
    let pending = (remaining-steps $run $PHASE3_CHECKPOINTS $after)
    verify-handoff $run
    if $handoff_only { return }
    for step in $pending {
        match $step {
            "vm3-started" => { vm3-started $run }
            "vm3-enrollment-requested" => { vm3-enrollment-requested $run }
            "vm3-enrolled" => { vm3-enrolled $run }
            "vm3-full-submitted" => { vm3-full-submitted $run }
            "vm3-full" => { vm3-full $run }
            _ => { error make {msg: $"Checkpoint has no implemented step: ($step)"} }
        }
        checkpoint $run 3 $step
    }
    error make {msg: "Phase 3 third full-stable promotion/bootstrap retirement, demotion and kexec acceptance remain unimplemented; VM3 is retained only with --keep-vms"}
}
