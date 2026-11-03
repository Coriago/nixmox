# Starts only the private VM; stops its session on test success/failure.
# For interrupted drivers, use: nu test/scripts/stop.nu RUN_DIRECTORY
use helpers.nu [guest]

def main [
    --ref: string = "dev/rewrite-foundation"
    --repository: string = "https://github.com/Coriago/nixmox.git"
] {
    do --capture-errors { ^python3 -c 'import socket; sockets = [socket.socket(), socket.socket()]; [s.bind(("127.0.0.1", p)) for s, p in zip(sockets, [22221, 18081])]' }
    let root = ($env.FILE_PWD | path join "../.." | path expand)
    mkdir ($root | path join "test/runs")
    let run = (^mktemp -d ($root | path join "test/runs/phase1-XXXXXX") | str trim)
    mkdir ($run | path join "public")
    print $"Artifacts: ($run)"
    ^ssh-keygen -q -t ed25519 -N "" -f ($run | path join "id_ed25519")
    cp ($run | path join "id_ed25519.pub") ($run | path join "public/id_ed25519.pub")
    do --capture-errors { ^go build -o ($run | path join "nixmox") ./cmd/nixmox }
    $env.PATH = ($env.PATH | prepend $run)

    print "Building VM1 from the working tree; GitOps still uses GitHub"
    do --capture-errors { ^nix build $"path:($root)/test/repo#vm1" --override-input nixmox $"git+file://($root)" --no-write-lock-file --out-link ($run | path join "vm") }
    let runner = ($run | path join "vm/bin/run-vm1-vm")
    $env.NIX_DISK_IMAGE = ($run | path join "vm1.qcow2")
    $env.NIXMOX_E2E_PUBLIC = ($run | path join "public")
    $env.QEMU_OPTS = $"-pidfile ($run)/qemu.pid"
    # The session contains only this VM and its read-only public-key virtiofs helper.
    ^setsid --fork sh -c 'echo $$ > "$1/session.pid"; exec "$2" > "$1/console.log" 2>&1 < /dev/null' sh $run $runner
    let failure = try {
        mut ready = false
        for attempt in 0..119 {
            let probe = (^nixmox --endpoint http://127.0.0.1:18081 --json health | complete)
            if $probe.exit_code == 0 { $ready = true; break }
            sleep 2sec
        }
        if not $ready { error make {msg: "VM daemon did not become ready within four minutes"} }
        do --capture-errors { ^nu ($root | path join "test/scripts/phase1.nu") $run --ref $ref --repository $repository }
        ""
    } catch {|err|
        $err.msg | save --force ($run | path join "failure.txt")
        try { guest $run "journalctl -u nixmox --no-pager -n 200" | save --force ($run | path join "daemon.log") }
        $err.msg
    }
    ^nu ($root | path join "test/scripts/stop.nu") $run
    if $failure != "" { error make {msg: $"Phase 1 failed: ($failure). Artifacts: ($run)"} }
}
