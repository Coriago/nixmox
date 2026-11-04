# The suite owns infrastructure. --after is an explicit operator-selected checkpoint.
use helpers.nu [guest record remaining-steps]
use std/assert
use vms.nu [start-vm wait-daemon]
use telemetry.nu [start-collector wait-collector wait-telemetry]
use phase1.nu PHASE1_CHECKPOINTS
use phase2.nu PHASE2_CHECKPOINTS
use phase3.nu PHASE3_CHECKPOINTS

# Reuse only the retained process; never silently replace a missing guest on resume.
def prepare-vm [root: path, run: path, name: string, --reuse] {
    let machine = ((open ($run | path join "vms.json")).machines | get $name)
    let directory = if $name == "vm1" { $run } else { $run | path join $name }
    let endpoint = $"http://127.0.0.1:($machine.httpPort)"
    if $reuse {
        print --stderr $"\n=== Suite | Reuse retained ($name) ===\n"
        let session = (open --raw ($directory | path join "session.pid") | str trim)
        let process = (^ps -s $session -o args= | complete)
        if $process.exit_code != 0 or not ($process.stdout | str contains $"($directory)/") {
            error make {msg: $"Retained ($name) is not running; inspect the run before restarting"}
        }
        print --stderr $"+ nixmox --endpoint ($endpoint) --json health"
        let health = (^nixmox --endpoint $endpoint --json health | from json | tee { to json --indent 2 | print --stderr })
        assert equal $health.nodeId (open ($run | path join $"telemetry-($name).json")).nodeId
        return
    }

    print --stderr $"\n=== Suite | Start ($name) ===\n"
    start-vm $root $directory $name ($run | path join "public") ($run | path join "vms.json")
    let health = (wait-daemon $endpoint)
    print --stderr $"\n=== Telemetry | Confirm ($name) has no collector service ===\n"
    assert equal (guest $run "systemctl show opentelemetry-collector.service --property=LoadState --value" --port $machine.sshPort) "not-found"
    let telemetry = (wait-telemetry $run $health.nodeId)
    record $run $"telemetry-($name)" $telemetry
    if $name != "vm1" {
        assert ($telemetry.hostId != (open ($run | path join "telemetry-vm1.json")).hostId) "Guests must have distinct telemetry identities"
    }
}

def main [
    --through: int = 1
    --keep-vms
    --resume: path
    --after: string = ""
    --ref: string = "dev/e2e-phase2"
    --repository: string = "https://github.com/Coriago/nixmox.git"
] {
    print --stderr "\n=== Suite | Validate requested flow and checkpoint ===\n"
    if $through not-in [1 2 3] { error make {msg: "--through must be 1, 2 or 3"} }
    if ($resume == null) != ($after == "") {
        error make {msg: "Resume requires both --resume RUN_DIRECTORY and --after CHECKPOINT"}
    }
    let root = ($env.FILE_PWD | path join "../.." | path expand)
    let checkpoints = ($PHASE1_CHECKPOINTS | merge $PHASE2_CHECKPOINTS | merge $PHASE3_CHECKPOINTS)
    let resume_phase2 = ($after in ($PHASE2_CHECKPOINTS | columns))
    let resume_phase3 = ($after in ($PHASE3_CHECKPOINTS | columns))
    if ($through == 1 and $resume_phase2) or ($through < 3 and $resume_phase3) {
        error make {msg: "Checkpoint belongs to a later phase than --through"}
    }
    let retained = if $resume == null { "" } else { $resume | path expand }
    # Validate all skipped-step artifacts before touching processes or cleanup policy.
    remaining-steps $retained $checkpoints $after | ignore
    if $resume != null {
        for file in [vms.json nixmox id_ed25519 checkpoint.json] {
            if not ($retained | path join $file | path exists) { error make {msg: $"Missing retained artifact: ($file)"} }
        }
        print --stderr "Last recorded checkpoint (evidence only; not an automatic resume decision):"
        open ($retained | path join "checkpoint.json") | to json --indent 2 | print --stderr
        print --stderr $"RESUME AFTER: ($after). Skipped assertions are not replayed; operator has reviewed the retained state."
    }
    let topology = if $resume == null { $root | path join "test/vms.json" } else { $retained | path join "vms.json" }
    let machines = (open $topology).machines
    let reuse_vm2 = ($resume != null and ($retained | path join "vm2/session.pid" | path exists))
    if ($resume_phase2 or $resume_phase3) and not $reuse_vm2 { error make {msg: "This checkpoint requires the retained VM2 session"} }
    let reuse_vm3 = ($resume != null and ($retained | path join "vm3/session.pid" | path exists))
    if $resume_phase3 and not $reuse_vm3 { error make {msg: "A phase-3 checkpoint requires the retained VM3 session"} }
    if $through == 3 and $machines.vm3.package? == null {
        error make {msg: "Saved topology predates VM3 boot support; start a fresh suite rather than rewriting retained evidence"}
    }

    print --stderr "\n=== Suite | Check ports for new infrastructure only ===\n"
    let first = if $resume == null { [$machines.vm1.sshPort $machines.vm1.httpPort] | append $machines.vm1.listeners } else { [] }
    let vm_ports = if $through >= 2 and not $reuse_vm2 { $first | append [$machines.vm2.sshPort $machines.vm2.httpPort] } else { $first }
    let all_vm_ports = if $through == 3 and not $reuse_vm3 { $vm_ports | append [$machines.vm3.sshPort $machines.vm3.httpPort] } else { $vm_ports }
    let ports = if $resume == null { $all_vm_ports | append [14318 14333] } else { $all_vm_ports }
    do --capture-errors {
        ^python3 -c 'import socket,sys; ports=list(map(int,sys.argv[1:])); sockets=[socket.socket() for _ in ports]; [s.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1) for s in sockets]; [s.bind(("127.0.0.1",p)) for s,p in zip(sockets,ports)]' ...$ports
    }

    print --stderr "\n=== Suite | Prepare the run directory ===\n"
    mkdir ($root | path join "test/runs")
    let run = if $resume == null { ^mktemp -d ($root | path join "test/runs/suite-XXXXXX") | str trim } else { $retained }
    print $"Artifacts: ($run)"
    if $resume == null {
        cp $topology ($run | path join "vms.json")
        mkdir ($run | path join "public")
        print --stderr "\n=== Suite | Generate the SSH test key and build the CLI ===\n"
        do --capture-errors {
            ^ssh-keygen -q -t ed25519 -N "" -f ($run | path join "id_ed25519")
            ^go build -o ($run | path join "nixmox") ./cmd/nixmox
        }
        cp ($run | path join "id_ed25519.pub") ($run | path join "public/id_ed25519.pub")
    }
    $env.PATH = ($env.PATH | prepend $run)
    let failure = try {
        if $resume == null { start-collector $root $run } else { wait-collector $run }
        $env.OTEL_EXPORTER_OTLP_ENDPOINT = "http://127.0.0.1:14318"
        prepare-vm $root $run vm1 --reuse=($resume != null)

        if not ($resume_phase2 or $resume_phase3) {
            print --stderr "\n=== Suite | Run phase 1 ===\n"
            do --capture-errors {
                ^nu ($root | path join "test/scripts/phase1.nu") $run --after $after --ref $ref --repository $repository
            }
        }
        if $through >= 2 {
            print --stderr "\n=== Suite | Continue phase 2 with the same VM1 ===\n"
            if not ($resume_phase2 or $resume_phase3) {
                do --capture-errors {
                    ^nu ($root | path join "test/scripts/phase2.nu") $run --handoff-only
                }
            }
            prepare-vm $root $run vm2 --reuse=$reuse_vm2
            let phase2_after = if $resume_phase3 { "vm2-promoted" } else if $resume_phase2 { $after } else { "" }
            do --capture-errors {
                ^nu ($root | path join "test/scripts/phase2.nu") $run --after $phase2_after
            }
        }
        if $through == 3 {
            print --stderr "\n=== Suite | Continue phase 3 with the same VM1 and VM2 ===\n"
            do --capture-errors {
                ^nu ($root | path join "test/scripts/phase3.nu") $run --handoff-only
            }
            prepare-vm $root $run vm3 --reuse=$reuse_vm3
            let phase3_after = if $resume_phase3 { $after } else { "" }
            do --capture-errors {
                ^nu ($root | path join "test/scripts/phase3.nu") $run --after $phase3_after
            }
        }
        ""
    } catch {|err|
        print --stderr "\n=== Suite | Capture failure diagnostics ===\n"
        $err.msg | save --force ($run | path join "failure.txt")
        try { guest $run "journalctl -u nixmox --no-pager -n 200" | save --force ($run | path join "daemon.log") }
        $err.msg
    }

    record $run "result" {through: $through, after: $after, keepVMs: $keep_vms, success: ($failure == ""), error: $failure}
    if $keep_vms {
        print --stderr "\n=== Suite | Keep infrastructure for inspection or explicit checkpoint resume ===\n"
        print $"Stop explicitly: nu test/scripts/stop.nu ($run)"
    } else {
        print --stderr "\n=== Suite | Stop suite-owned infrastructure ===\n"
        do --capture-errors { ^nu ($root | path join "test/scripts/stop.nu") $run }
    }
    if $failure != "" { error make {msg: $"Suite failed: ($failure). Artifacts: ($run)"} }
}
