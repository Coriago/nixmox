# VM lifecycle belongs to the suite, never to an individual phase.
export def start-vm [root: path, run: path, package: string, name: string, public: path] {
    mkdir $run
    mkdir ($run | path join "tmp")
    do --capture-errors { ^nix build $"path:($root)/test/repo#($package)" --override-input nixmox $"git+file://($root)" --no-write-lock-file --out-link ($run | path join "vm") }
    let disk = if $package == "vm2-installer" { "" } else { $run | path join $"($name).qcow2" }
    with-env {
        NIX_DISK_IMAGE: $disk
        NIXMOX_E2E_PUBLIC: $public
        TMPDIR: ($run | path join "tmp")
        USE_TMPDIR: "1"
        QEMU_OPTS: $"-pidfile ($run)/qemu.pid"
    } {
        do --capture-errors {
            ^setsid --fork sh -c 'echo $$ > "$1/session.pid"; exec "$2" > "$1/console.log" 2>&1 < /dev/null' sh $run ($run | path join $"vm/bin/run-($name)-vm")
        }
    }
}

export def wait-daemon [endpoint: string] {
    for attempt in 0..119 {
        print --stderr $"+ nixmox --endpoint ($endpoint) --json health"
        let probe = (^nixmox --endpoint $endpoint --json health | complete | inspect)
        if $probe.exit_code == 0 { return }
        sleep 2sec
    }
    error make {msg: $"Daemon ($endpoint) did not become ready within four minutes"}
}
