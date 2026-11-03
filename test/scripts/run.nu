# Phase 1 can run alone; phase 2 always inherits its live VM1.
# Interrupted runs can be stopped with: nu test/scripts/stop.nu RUN_DIRECTORY
use helpers.nu [guest record]
use std/assert
use vms.nu [start-vm wait-daemon]
use telemetry.nu [start-collector wait-collector wait-telemetry]

def main [
    --through: int = 1
    --keep-vms
    --resume: path
    --ref: string = "dev/e2e-phase2"
    --repository: string = "https://github.com/Coriago/nixmox.git"
] {
    print --stderr "\n=== Suite | Validate requested flow and available ports ===\n"

    if $through not-in [1 2] {
        error make {msg: "--through must be 1 or 2"}
    }
    if $resume != null and $through != 2 {
        error make {msg: "--resume continues a retained phase-1 run into phase 2"}
    }
    let root = ($env.FILE_PWD | path join "../.." | path expand)
    let machines = (open ($root | path join "test/vms.json")).machines
    let first = if $resume == null { [$machines.vm1.sshPort $machines.vm1.httpPort] | append $machines.vm1.listeners } else { [] }
    let vm_ports = if $through == 2 { $first | append [$machines.vm2.sshPort $machines.vm2.httpPort] } else { $first }
    let ports = if $resume == null { $vm_ports | append [14318 14333] } else { $vm_ports }
    # Match QEMU's SO_REUSEADDR behavior without treating TCP TIME_WAIT as a live VM.
    do --capture-errors {
        ^python3 -c 'import socket,sys; ports=list(map(int,sys.argv[1:])); sockets=[socket.socket() for _ in ports]; [s.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1) for s in sockets]; [s.bind(("127.0.0.1",p)) for s,p in zip(sockets,ports)]' ...$ports
    }

    print --stderr "\n=== Suite | Prepare the run directory ===\n"

    mkdir ($root | path join "test/runs")
    let run = if $resume == null {
        ^mktemp -d ($root | path join "test/runs/suite-XXXXXX") | str trim
    } else { $resume | path expand }
    print $"Artifacts: ($run)"
    if $resume == null {
        print --stderr "\n=== Suite | Generate the SSH test key ===\n"

        cp ($root | path join "test/vms.json") ($run | path join "vms.json")
        mkdir ($run | path join "public")
        do --capture-errors {
            ^ssh-keygen -q -t ed25519 -N "" -f ($run | path join "id_ed25519")
        }
        cp ($run | path join "id_ed25519.pub") ($run | path join "public/id_ed25519.pub")

        print --stderr "\n=== Suite | Build the CLI ===\n"

        do --capture-errors {
            ^go build -o ($run | path join "nixmox") ./cmd/nixmox
        }
    } else {
        print --stderr "\n=== Suite | Check retained phase-1 artifacts ===\n"

        if not ($run | path join "phase1.json" | path exists) {
            error make {msg: "A successfully completed phase 1 is required"}
        }
        if ($run | path join "vm2/session.pid" | path exists) {
            error make {msg: "Phase 2 already started; inspect its retained state rather than replaying enrollment"}
        }
    }
    $env.PATH = ($env.PATH | prepend $run)
    let failure = try {
        if $resume == null {
            start-collector $root $run
        } else {
            print --stderr "\n=== Telemetry | Verify the retained collector ===\n"
            wait-collector $run
        }
        $env.OTEL_EXPORTER_OTLP_ENDPOINT = "http://127.0.0.1:14318"

        if $resume == null {
            print --stderr "\n=== Suite | Start VM1 ===\n"

            start-vm $root $run vm1 ($run | path join "public")
            let health = (wait-daemon "http://127.0.0.1:18081")
            print --stderr "\n=== Telemetry | Confirm VM1 has no collector service ===\n"
            assert equal (guest $run "systemctl show opentelemetry-collector.service --property=LoadState --value") "not-found"
            record $run "telemetry-vm1" (wait-telemetry $run $health.nodeId)

            print --stderr "\n=== Suite | Run phase 1 ===\n"

            do --capture-errors {
                ^nu ($root | path join "test/scripts/phase1.nu") $run --ref $ref --repository $repository
            }
        } else {
            print --stderr "\n=== Suite | Verify retained VM1 before continuing ===\n"

            # Verify live identity before launching anything or mutating cluster state.
            do --capture-errors {
                ^nu ($root | path join "test/scripts/phase2.nu") $run --handoff-only
            }
        }
        if $through == 2 {
            print --stderr "\n=== Suite | Continue to phase 2 with VM1 still running ===\n"

            # No stop, restart or new cluster between the phases.
            start-vm $root ($run | path join "vm2") vm2 ($run | path join "public")
            let health = (wait-daemon "http://127.0.0.1:18082")
            print --stderr "\n=== Telemetry | Confirm VM2 has no collector service ===\n"
            assert equal (guest $run "systemctl show opentelemetry-collector.service --property=LoadState --value" --port $machines.vm2.sshPort) "not-found"
            let telemetry = (wait-telemetry $run $health.nodeId)
            record $run "telemetry-vm2" $telemetry
            assert ($telemetry.hostId != (open ($run | path join "telemetry-vm1.json")).hostId) "Guests must have distinct telemetry identities"

            print --stderr "\n=== Suite | Run phase 2 ===\n"

            do --capture-errors {
                ^nu ($root | path join "test/scripts/phase2.nu") $run
            }
        }
        ""
    } catch {|err|
        print --stderr "\n=== Suite | Capture failure diagnostics ===\n"

        $err.msg | save --force ($run | path join "failure.txt")
        try { guest $run "journalctl -u nixmox --no-pager -n 200" | save --force ($run | path join "daemon.log") }
        $err.msg
    }

    print --stderr "\n=== Suite | Save the test result ===\n"

    {through: $through, keepVMs: $keep_vms, success: ($failure == ""), error: $failure}
        | to json | save --force ($run | path join "result.json")
    if $keep_vms {
        print --stderr "\n=== Suite | Keep VMs running ===\n"

        print $"Automatic VM cleanup disabled. Stop explicitly: nu test/scripts/stop.nu ($run)"
    } else {
        print --stderr "\n=== Suite | Stop suite-owned VMs ===\n"

        do --capture-errors {
            ^nu ($root | path join "test/scripts/stop.nu") $run
        }
    }
    if $failure != "" {
        error make {msg: $"Suite failed: ($failure). Artifacts: ($run)"}
    }
}
