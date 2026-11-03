# Phase 1 can run alone; phase 2 always inherits its live VM1.
# Interrupted runs can be stopped with: nu test/scripts/stop.nu RUN_DIRECTORY
use helpers.nu [guest]
use vms.nu [start-vm wait-daemon]

def main [
    --through: int = 1
    --keep-vms
    --resume: path
    --ref: string = "dev/e2e-phase2"
    --repository: string = "https://github.com/Coriago/nixmox.git"
] {
    if $through not-in [1 2] { error make {msg: "--through must be 1 or 2"} }
    if $resume != null and $through != 2 { error make {msg: "--resume continues a retained phase-1 run into phase 2"} }
    if $resume == null {
        do --capture-errors { ^python3 -c 'import socket,sys; ports=[22221,18081,23222,23223]+([22222,18082] if sys.argv[1]=="2" else []); sockets=[socket.socket() for _ in ports]; [s.bind(("127.0.0.1",p)) for s,p in zip(sockets,ports)]' ($through | into string) }
    }
    let root = ($env.FILE_PWD | path join "../.." | path expand)
    mkdir ($root | path join "test/runs")
    let run = if $resume == null {
        ^mktemp -d ($root | path join "test/runs/suite-XXXXXX") | str trim
    } else { $resume | path expand }
    print $"Artifacts: ($run)"
    if $resume == null {
        mkdir ($run | path join "public")
        do --capture-errors { ^ssh-keygen -q -t ed25519 -N "" -f ($run | path join "id_ed25519") }
        cp ($run | path join "id_ed25519.pub") ($run | path join "public/id_ed25519.pub")
        do --capture-errors { ^go build -o ($run | path join "nixmox") ./cmd/nixmox }
    } else {
        if not ($run | path join "phase1.json" | path exists) { error make {msg: "A successfully completed phase 1 is required"} }
        if ($run | path join "vm2/session.pid" | path exists) { error make {msg: "Phase 2 already started; inspect its retained state rather than replaying enrollment"} }
    }
    $env.PATH = ($env.PATH | prepend $run)
    let failure = try {
        if $resume == null {
            start-vm $root $run vm1 ($run | path join "public")
            wait-daemon "http://127.0.0.1:18081"
            do --capture-errors { ^nu ($root | path join "test/scripts/phase1.nu") $run --ref $ref --repository $repository }
        } else {
            # Verify live identity before launching anything or mutating cluster state.
            do --capture-errors { ^nu ($root | path join "test/scripts/phase2.nu") $run --handoff-only }
        }
        if $through == 2 {
            # No stop, restart or new cluster between the phases.
            start-vm $root ($run | path join "vm2") vm2 ($run | path join "public")
            wait-daemon "http://127.0.0.1:18082"
            do --capture-errors { ^nu ($root | path join "test/scripts/phase2.nu") $run }
        }
        ""
    } catch {|err|
        $err.msg | save --force ($run | path join "failure.txt")
        try { guest $run "journalctl -u nixmox --no-pager -n 200" | save --force ($run | path join "daemon.log") }
        $err.msg
    }
    {through: $through, keepVMs: $keep_vms, success: ($failure == ""), error: $failure}
        | to json | save --force ($run | path join "result.json")
    if $keep_vms {
        print $"Automatic VM cleanup disabled. Stop explicitly: nu test/scripts/stop.nu ($run)"
    } else {
        do --capture-errors { ^nu ($root | path join "test/scripts/stop.nu") $run }
    }
    if $failure != "" { error make {msg: $"Suite failed: ($failure). Artifacts: ($run)"} }
}
