# VM2 only; VM3 and quorum/kexec belong to phase3.nu.
use std/assert
use helpers.nu [guest record checkpoint remaining-steps wait-task]

export const PHASE2_CHECKPOINTS = {
    "vm2-started": ["phase2-installer.json" "telemetry-vm2.json"]
    "vm2-discovered": ["phase2-discovery.json"]
    "vm2-enrolled": ["phase2-enrollment.json" "phase2-pending.json"]
    "vm2-configured": ["phase2-vm2-configuration.json" "phase2-vm2-rendered.json"]
    "vm2-installation-submitted": ["phase2-installation-submitted.json" "phase2-preservation-before.json"]
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

def vm2-installation-submitted [run: path, endpoint: string] {
    let machine = (open ($run | path join "vms.json")).machines.vm2
    let node = (open ($run | path join "phase2-pending.json")).nodeId
    let configured = (open ($run | path join "phase2-vm2-configuration.json"))
    print --stderr "\n=== Phase 2 | Verify rescue disk, VM control and identity before installation ===\n"
    assert ($run | path join "vm2/vm2.qcow2" | path exists) "Persistent installer rescue disk required"
    let recovery = (^python3 -c '
import json,socket,sys
s=socket.socket(socket.AF_UNIX);s.settimeout(5);s.connect(sys.argv[1]);f=s.makefile("rwb")
json.loads(f.readline())
def call(command,arguments=None):
    f.write((json.dumps({"execute":command,"arguments":arguments or {}})+"\n").encode());f.flush()
    while True:
        response=json.loads(f.readline())
        if "error" in response: raise RuntimeError(response["error"])
        if "return" in response: return response["return"]
call("qmp_capabilities")
selected=[]
for group in ["/machine/peripheral","/machine/peripheral-anon"]:
    for device in call("qom-list",{"path":group}):
        if device["type"] != "child<virtio-blk-pci>": continue
        path=group+"/"+device["name"]
        if call("qom-get",{"path":path,"property":"serial"}) == sys.argv[2]: selected.append(path)
if len(selected)!=1: raise RuntimeError("unique installation disk required")
if call("qom-get",{"path":selected[0],"property":"bootindex"}) != 0:
    call("qom-set",{"path":selected[0],"property":"bootindex","value":0})
status=call("query-status")
status["targetBootIndex"]=call("qom-get",{"path":selected[0],"property":"bootindex"})
print(json.dumps(status))
' ($run | path join "vm2/qmp.sock") $machine.diskSerial | from json)
    assert equal $recovery.targetBootIndex 0
    assert $recovery.running
    let before = {
        bootId: (guest $run "cat /proc/sys/kernel/random/boot_id" --port $machine.sshPort)
        identityHashes: (guest $run "sha256sum /var/lib/nixmox/node.key /etc/ssh/ssh_host_ed25519_key /var/lib/nixmox/age.key | cut -d ' ' -f1" --port $machine.sshPort)
        recovery: $recovery
    }
    record $run "phase2-preservation-before" $before
    print --stderr $"+ nixmox --endpoint ($endpoint) host configuration ($node) --configuration ($configured.id) --version ($configured.version)"
    do --capture-errors { ^nixmox --endpoint $endpoint host configuration $node --configuration $configured.id --version $configured.version }
    print --stderr $"+ nixmox --endpoint ($endpoint) --json deploy --host ($node) --configuration ($configured.id) --version ($configured.version) --approve"
    let submitted = (^nixmox --endpoint $endpoint --json deploy --host $node --configuration $configured.id --version $configured.version --approve | from json | tee { to json --indent 2 | print --stderr })
    assert equal $submitted.tasktype "fresh-deployment"
    assert $submitted.persist
    record $run "phase2-installation-submitted" $submitted
}

def vm2-installed [run: path, endpoint: string] {
    let machine = (open ($run | path join "vms.json")).machines.vm2
    let target = $"http://127.0.0.1:($machine.httpPort)"
    let pending = (open ($run | path join "phase2-pending.json"))
    let before = (open ($run | path join "phase2-preservation-before.json"))
    let submitted = (open ($run | path join "phase2-installation-submitted.json"))
    let installed = (wait-task $endpoint $submitted.id)
    print --stderr "\n=== Phase 2 | Verify installed boot, identity recovery and full client role ===\n"
    let boot = (guest $run "cat /proc/sys/kernel/random/boot_id" --port $machine.sshPort)
    assert ($boot != $before.bootId)
    assert equal (guest $run "sha256sum /var/lib/nixmox/node.key /etc/ssh/ssh_host_ed25519_key /var/lib/nixmox/age.key | cut -d ' ' -f1" --port $machine.sshPort) $before.identityHashes
    assert equal (guest $run "readlink -f /run/current-system" --port $machine.sshPort) $installed.result.storePath
    assert equal (guest $run "hostname" --port $machine.sshPort) "vm2"
    assert equal (guest $run "nixmox-e2e-testpkg" --port $machine.sshPort) "nixmox-e2e-testpkg-v1"
    assert equal $installed.result.generation 1
    assert equal $installed.result.membershipStatus "full"
    for role in [build finalize promotion] { assert ($role in $installed.links.childTasks.role) }
    print --stderr $"+ nixmox --endpoint ($endpoint) --json host show ($pending.nodeId)"
    let member = (^nixmox --endpoint $endpoint --json host show $pending.nodeId | from json | tee { to json --indent 2 | print --stderr })
    assert equal $member.status "full"
    assert equal $member.environment "installed"
    assert equal $member.clusterId $pending.clusterId
    assert equal $member.agePublicKey $pending.agePublicKey
    assert equal $member.enrollmentRequestId $pending.enrollmentRequestId
    print --stderr $"+ nixmox --endpoint ($target) --json health"
    let health = (^nixmox --endpoint $target --json health | from json | tee { to json --indent 2 | print --stderr })
    assert equal $health.nodeId $pending.nodeId
    assert equal $health.checks.natsClient.status "healthy"
    assert equal $health.checks.natsServer.status "not-applicable"
    print --stderr $"+ nixmox --endpoint ($target) --json cache show"
    let cache = (^nixmox --endpoint $target --json cache show | from json | tee { to json --indent 2 | print --stderr })
    assert equal $cache.publicKey (open ($run | path join "04-cache.json")).publicKey
    assert ((guest $run "curl --fail --silent http://127.0.0.1:8080/store-cache/nix-cache-info" --port $machine.sshPort) | str contains "StoreDir: /nix/store")
    record $run "phase2-installation" {task: $installed, member: $member, health: $health, cache: $cache, bootId: $boot}
}

def vm2-promoted [run: path, endpoint: string] {
    let installed = (open ($run | path join "phase2-installation.json"))
    let machine = (open ($run | path join "vms.json")).machines.vm2
    let target = $"http://127.0.0.1:($machine.httpPort)"
    print --stderr "\n=== Phase 2 | Explicitly promote the installed full member to full-stable ===\n"
    let daemonPIDs = {
        vm1: (guest $run "systemctl show nixmox --property=MainPID --value")
        vm2: (guest $run "systemctl show nixmox --property=MainPID --value" --port $machine.sshPort)
    }
    assert ($daemonPIDs.vm1 != "0" and $daemonPIDs.vm2 != "0")
    print --stderr $"+ nixmox --endpoint ($endpoint) --json host promote ($installed.member.nodeId) --to full-stable"
    let submitted = (^nixmox --endpoint $endpoint --json host promote $installed.member.nodeId --to full-stable | from json | tee { to json --indent 2 | print --stderr })
    record $run "phase2-promotion-submitted" $submitted
    let promotion = (wait-task $endpoint $submitted.id)
    assert equal $promotion.result.membershipStatus "full-stable"
    assert equal $promotion.result.replicas 3
    assert equal $promotion.context.previousStatus "full"
    print --stderr $"+ nixmox --endpoint ($endpoint) --json host show ($installed.member.nodeId)"
    let member = (^nixmox --endpoint $endpoint --json host show $installed.member.nodeId | from json | tee { to json --indent 2 | print --stderr })
    assert equal $member.status "full-stable"
    assert equal $member.clusterId $installed.member.clusterId
    print --stderr $"+ nixmox --endpoint ($target) --json health"
    let health = (^nixmox --endpoint $target --json health | from json | tee { to json --indent 2 | print --stderr })
    assert equal $health.nodeId $member.nodeId
    for check in [natsClient natsServer cluster] { assert equal ($health.checks | get $check).status "healthy" }
    assert equal (guest $run "cat /proc/sys/kernel/random/boot_id" --port $machine.sshPort) $installed.bootId
    assert ((guest $run "ss -lntH 'sport = :4222'" --port $machine.sshPort) | is-not-empty)
    assert ((guest $run "ss -lntH 'sport = :4223'" --port $machine.sshPort) | is-empty)
    assert ((guest $run "ss -lntH 'sport = :4223'") | is-not-empty)
    print --stderr "\n=== Phase 2 | Require healthy peers without process restarts after promotion ===\n"
    # Role changes reopen the runtime in-process. A systemd restart is recovery
    # from a failure, not a successful membership transition.
    for attempt in 0..4 {
        sleep 3sec
        assert equal (guest $run "systemctl show nixmox --property=MainPID --value") $daemonPIDs.vm1
        assert equal (guest $run "systemctl show nixmox --property=MainPID --value" --port $machine.sshPort) $daemonPIDs.vm2
        for peer in [$endpoint $target] {
            print --stderr $"+ nixmox --endpoint ($peer) --json health"
            let stable = (^nixmox --endpoint $peer --json health | from json)
            for check in [natsClient natsServer cluster] { assert equal ($stable.checks | get $check).status "healthy" }
        }
    }
    record $run "phase2" {endpoint: $endpoint, nodeId: $member.nodeId, clusterId: $member.clusterId, member: $member, health: $health, promotion: $promotion, bootId: $installed.bootId, daemonPIDs: $daemonPIDs}
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
            "vm2-installation-submitted" => { vm2-installation-submitted $run $endpoint }
            "vm2-installed" => { vm2-installed $run $endpoint }
            "vm2-promoted" => { vm2-promoted $run $endpoint }
            _ => { error make {msg: $"Checkpoint has no implemented step: ($step)"} }
        }
        checkpoint $run 2 $step
    }
    open ($run | path join "phase2.json") | ignore
    print "PASS: phase 2"
}
