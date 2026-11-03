# Stop guests first; stop the suite collector last so it can flush its files.
use helpers.nu [guest]

def stop-session [directory: path, --suite: path, --port: int, --wait] {
    print --stderr $"\n=== Cleanup | Check session in ($directory) ===\n"

    let file = ($directory | path join "session.pid")
    if not ($file | path exists) { return }

    let session = (open --raw $file | str trim | into int)
    let processes = (^ps -s ($session | into string) -o args= | complete)
    if $processes.exit_code != 0 { return }
    if not ($processes.stdout | str contains $"($directory)/") {
        error make {msg: $"Refusing to stop an unrelated session: ($session)"}
    }

    if $port != null {
        print --stderr "\n=== Cleanup | Flush guest daemon telemetry ===\n"

        try {
            guest $suite "timeout 20s systemctl stop nixmox.service" --port $port | ignore
        } catch {|err|
            # Failed/partially booted guests must not prevent cleanup of the suite.
            print --stderr $"WARNING: guest telemetry drain failed: ($err.msg)"
        }
    }

    print --stderr $"\n=== Cleanup | Stop verified session ($session) ===\n"

    do --capture-errors {
        ^python3 -c 'import os, signal, sys; os.killpg(int(sys.argv[1]), signal.SIGTERM)' ($session | into string)
    }

    if $wait {
        for attempt in 0..99 {
            let state = (^ps -s ($session | into string) -o stat= | complete)
            if $state.exit_code != 0 or ($state.stdout | lines | all {|line| $line | str trim | str starts-with "Z"}) { return }
            sleep 100ms
        }
        error make {msg: "Collector did not finish shutdown within ten seconds"}
    }
}

def main [run: path] {
    let run = ($run | path expand)
    let collector = ($run | path join "telemetry")
    let drain = ($collector | path join "session.pid" | path exists)
    let machines = if ($run | path join "vms.json" | path exists) { (open ($run | path join "vms.json")).machines } else { {} }
    let vms = [
        {name: vm1, directory: $run}
        {name: vm2, directory: ($run | path join "vm2")}
        {name: vm3, directory: ($run | path join "vm3")}
    ]
    let failures = ($vms | each {|vm|
        try {
            if $drain and ($vm.name in ($machines | columns)) {
                let port = ($machines | get $vm.name).sshPort
                stop-session $vm.directory --suite $run --port $port
            } else {
                stop-session $vm.directory
            }
            null
        } catch {|err| $err.msg}
    } | compact)

    let collector_failure = try {
        stop-session $collector --wait
        null
    } catch {|err| $err.msg}

    let failures = ($failures | append $collector_failure | compact)
    if ($failures | is-not-empty) { error make {msg: ($failures | str join "\n")} }
}
