# Physical VM wiring only; no hostnames, guest addresses or installed configuration.
export def start-vm [root: path, run: path, name: string, public: path] {
    print --stderr "\n=== VM | Load machine wiring ===\n"

    let topology = (open ($root | path join "test/vms.json"))
    let machine = ($topology.machines | get $name)
    mkdir $run
    mkdir ($run | path join "tmp")

    print --stderr $"\n=== VM | Build ($name) boot package ===\n"

    do --capture-errors {
        ^nix build $"path:($root)/test/repo#($machine.package)" --override-input nixmox $"git+file://($root)" --no-write-lock-file --out-link ($run | path join "vm")
    }

    print --stderr $"\n=== VM | Prepare ($name) network devices ===\n"

    mut options = [$"-pidfile ($run)/qemu.pid" $"-device virtio-net-pci,netdev=cluster,mac=($machine.mac)"]
    if $machine.listeners? != null {
        $options = ($options | append [
            "-netdev hubport,id=cluster,hubid=0"
        ])
        for port in $machine.listeners {
            $options = ($options | append [
                $"-netdev socket,id=peer($port),listen=127.0.0.1:($port)"
                $"-netdev hubport,id=port($port),hubid=0,netdev=peer($port)"
            ])
        }
    } else {
        $options = ($options | append $"-netdev socket,id=cluster,connect=127.0.0.1:($machine.connect)")
    }
    if $machine.diskSerial? != null {
        print --stderr $"\n=== VM | Prepare ($name) private installation disk ===\n"

        let target = ($run | path join "target.qcow2")
        if not ($target | path exists) {
            do --capture-errors {
                ^qemu-img create -f qcow2 $target $machine.diskSize
            }
        }
        $options = ($options | append [
            $"-drive file=($target),format=qcow2,if=none,id=target"
            $"-device virtio-blk-pci,drive=target,serial=($machine.diskSerial)"
        ])
    }

    print --stderr $"\n=== VM | Launch ($name) in its own session ===\n"

    let runner = (glob ($run | path join "vm/bin/run-*-vm") | first)
    let disk = if $machine.package == "installer" { "" } else { $run | path join $"($name).qcow2" }
    with-env {
        NIX_DISK_IMAGE: $disk
        NIXMOX_E2E_PUBLIC: $public
        TMPDIR: ($run | path join "tmp")
        USE_TMPDIR: "1"
        QEMU_OPTS: ($options | str join " ")
        QEMU_NET_OPTS: $"hostfwd=tcp:127.0.0.1:($machine.sshPort)-:22,hostfwd=tcp:127.0.0.1:($machine.httpPort)-:8080"
    } {
        do --capture-errors {
            ^setsid --fork sh -c 'echo $$ > "$1/session.pid"; exec "$2" > "$1/console.log" 2>&1 < /dev/null' sh $run $runner
        }
    }
}

export def wait-daemon [endpoint: string] {
    print --stderr $"\n=== VM | Wait for daemon at ($endpoint) ===\n"

    for attempt in 0..119 {
        print --stderr $"+ nixmox --endpoint ($endpoint) --json health"
        let probe = (
            ^nixmox --endpoint $endpoint --json health
            | complete
            | inspect
        )

        if $probe.exit_code == 0 {
            return
        }
        sleep 2sec
    }
    error make {msg: $"Daemon ($endpoint) did not become ready within four minutes"}
}
