# VM2 only; VM3 and quorum/kexec belong to phase3.nu.
use std/assert
use helpers.nu [guest record checkpoint remaining-steps]

export const PHASE2_CHECKPOINTS = {
    "vm2-started": ["phase2-installer.json" "telemetry-vm2.json"]
    "vm2-discovered": ["phase2-discovery.json"]
    "vm2-enrolled": ["phase2-enrollment.json" "phase2-pending.json"]
    "vm2-configured": ["phase2-vm2-configuration.json" "phase2-vm2-rendered.json"]
    "vm2-installed": ["phase2-installation.json"]
    "vm2-promoted": ["phase2.json"]
}

def verify-handoff [run: path] {
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
        | tee { to json --indent 2 | print --stderr }
    )

    assert equal $cluster.id $previous.clusterId
    print --stderr $"+ nixmox --endpoint ($endpoint) --json host show ($previous.nodeId)"
    let member = (
        ^nixmox --endpoint $endpoint --json host show $previous.nodeId
        | from json
        | tee { to json --indent 2 | print --stderr }
    )

    assert equal $member.daemon.startedAt $previous.daemonStartedAt
    assert equal $member.status "full-stable"
    record $run "phase2-handoff" $member
}

def vm2-started [run: path, endpoint: string] {
    let previous = (open ($run | path join "phase1.json"))
    print --stderr "\n=== Phase 2 | Verify the unjoined installer ===\n"

    let machine = (open ($run | path join "vms.json")).machines.vm2
    let installer = $"http://127.0.0.1:($machine.httpPort)"
    print --stderr $"+ nixmox --endpoint ($installer) --json health"
    let health = (
        ^nixmox --endpoint $installer --json health
        | from json
        | tee { to json --indent 2 | print --stderr }
    )

    assert ($health.nodeId != $previous.nodeId)
    assert equal $health.checks.cluster.status "not-applicable"
    assert equal $health.checks.natsServer.status "not-applicable"

    print --stderr "\n=== Phase 2 | Verify the private target disk ===\n"

    let disks = (guest $run "lsblk --json --bytes --output PATH,SERIAL,TYPE,SIZE" --port $machine.sshPort | from json)
    let target = ($disks.blockdevices | where serial == $machine.diskSerial | first)
    assert equal $target.type "disk"
    assert ($target.size >= 17179869184)
    let targetDevice = $"/dev/disk/by-id/virtio-($machine.diskSerial)"
    assert equal (guest $run $"readlink -f ($targetDevice)" --port $machine.sshPort) $target.path

    print --stderr "\n=== Phase 2 | Check guest-LAN connectivity in both directions ===\n"

    # Check the private guest LAN in both directions before asking for discovery.
    let from_vm2 = (guest $run "curl --fail --silent http://192.168.77.1:8080/health" --port $machine.sshPort | from json)
    let interfaces = (guest $run "ip -j address show" --port $machine.sshPort | from json)
    let lan = ($interfaces | where address == $machine.mac | first)
    let address = ($lan.addr_info | where family == "inet" and scope == "global" | first | get local)
    let contact = $"http://($address):8080"
    let from_vm1 = (guest $run $"curl --fail --silent ($contact)/health" | from json)
    assert equal $from_vm2.nodeId $previous.nodeId
    assert equal $from_vm1.nodeId $health.nodeId
    record $run "phase2-installer" {health: $health, disk: $target, targetDevice: $targetDevice, contact: $contact}
}

def vm2-discovered [run: path, endpoint: string] {
    let installer = (open ($run | path join "phase2-installer.json"))
    let health = $installer.health
    let contact = $installer.contact
    print --stderr "\n=== Phase 2 | Discover the installer ===\n"

    mut candidate: any = null
    for attempt in 0..14 {
        print --stderr $"+ nixmox --endpoint ($endpoint) --json discovery list"
        let discovery = (
            ^nixmox --endpoint $endpoint --json discovery list
            | from json
            | tee { to json --indent 2 | print --stderr }
        )
        let matches = ($discovery.items | where advertisement.nodeId == $health.nodeId)
        if ($matches | is-not-empty) {
            $candidate = ($matches | first)
            break
        }
        sleep 2sec
    }
    assert ($candidate != null) "Installer was not discovered within 30 seconds"
    assert equal $candidate.advertisement.clusterId null
    assert ($contact in $candidate.advertisement.endpoints)
    record $run "phase2-discovery" $candidate
}

def vm2-enrolled [run: path, endpoint: string] {
    let previous = (open ($run | path join "phase1.json"))
    let health = (open ($run | path join "phase2-installer.json")).health
    let candidate = (open ($run | path join "phase2-discovery.json"))
    print --stderr "\n=== Phase 2 | Enroll the discovered installer ===\n"

    # Use the discovered installer, not a manual-address enrollment shortcut.
    print --stderr $"+ nixmox --endpoint ($endpoint) --json enrollment add --discovery ($candidate.id) --contact ($previous.nodeId)"
    let request = (
        ^nixmox --endpoint $endpoint --json enrollment add --discovery $candidate.id --contact $previous.nodeId
        | from json
        | tee { to json --indent 2 | print --stderr }
    )

    record $run "phase2-enrollment" $request

    print --stderr "\n=== Phase 2 | Verify pending installer membership ===\n"

    print --stderr $"+ nixmox --endpoint ($endpoint) --json host show ($health.nodeId)"
    let pending = (
        ^nixmox --endpoint $endpoint --json host show $health.nodeId
        | from json
        | tee { to json --indent 2 | print --stderr }
    )

    assert equal $pending.status "pending"
    assert equal $pending.environment "installer"
    record $run "phase2-pending" $pending
}

def vm2-configured [run: path, endpoint: string] {
    let root = ($env.FILE_PWD | path join "../.." | path expand)
    let installer = (open ($run | path join "phase2-installer.json"))
    let health = $installer.health
    let targetDevice = $installer.targetDevice
    print --stderr "\n=== Phase 2 | Configure the host through the recipe workflow ===\n"

    do --capture-errors {
        ^nu ($root | path join "test/scripts/configure-host.nu") $run vm2 $health.nodeId $targetDevice
    }
}

def main [run: path, --after: string = "", --handoff-only] {
    let pending = (remaining-steps $run $PHASE2_CHECKPOINTS $after)
    if $handoff_only { verify-handoff $run; return }
    let endpoint = (open ($run | path join "phase1.json")).endpoint
    for step in $pending {
        match $step {
            "vm2-started" => { verify-handoff $run; vm2-started $run $endpoint }
            "vm2-discovered" => { vm2-discovered $run $endpoint }
            "vm2-enrolled" => { vm2-enrolled $run $endpoint }
            "vm2-configured" => { vm2-configured $run $endpoint }
            "vm2-installed" => { error make {msg: "Phase 2: fresh installation is not implemented; no checkpoint recorded"} }
            "vm2-promoted" => { error make {msg: "Phase 2: promotion and handoff are not implemented; no checkpoint recorded"} }
            _ => { error make {msg: $"Checkpoint has no implemented step: ($step)"} }
        }
        checkpoint $run 2 $step
    }
    open ($run | path join "phase2.json") | ignore
    print "PASS: phase 2"
}
