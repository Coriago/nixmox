# Shared transport, task polling and artifact output; phase scripts own domain operations.
export def guest [run: path, command: string, --port: int = 22221] {
    let result = (^ssh -F /dev/null -i ($run | path join "id_ed25519")
        -p $port -o BatchMode=yes -o ConnectTimeout=5
        -o StrictHostKeyChecking=accept-new
        -o $"UserKnownHostsFile=($run)/known_hosts"
        root@127.0.0.1 $command | complete)
    if $result.exit_code != 0 {
        error make {msg: $"guest command failed: ($result.stderr)"}
    }
    $result.stdout | str trim
}

export def wait-task [endpoint: string, id: string] {
    print --stderr $"\n=== Task | Wait for ($id) ===\n"

    print --stderr $"+ nixmox --endpoint ($endpoint) --json task show ($id) [poll until terminal status]"
    mut previous = ""
    for attempt in 0..899 {
        let task = (
            ^nixmox --endpoint $endpoint --json task show $id
            | from json
        )
        let response = ($task | to json --indent 2)
        if $response != $previous {
            print --stderr $response
            $previous = $response
        }

        match $task.status {
            "succeeded" => { return $task }
            "manual-review" => { error make {msg: $"task ($id) unexpectedly requires approval"} }
            "failed" | "unknown" | "cancelled" => { error make {msg: ($task | to json)} }
        }
        sleep 2sec
    }
    error make {msg: $"task ($id) exceeded 30 minutes"}
}

# --after is an operator decision. Check names/files, not live-state reconciliation.
export def remaining-steps [run: path, checkpoints: record, after: string] {
    let names = ($checkpoints | columns)
    if $after == "" { return $names }
    if $after not-in $names { error make {msg: $"Unknown checkpoint: ($after). Available: ($names | str join ', ')"} }
    let remaining = ($names | skip until {|name| $name == $after} | skip 1)
    for name in ($names | where {|name| $name not-in $remaining}) {
        for file in ($checkpoints | get $name) {
            if not ($run | path join $file | path exists) {
                error make {msg: $"Checkpoint ($name) requires artifact ($file); inspect the run before resuming"}
            }
        }
    }
    $remaining
}

export def checkpoint [run: path, phase: int, name: string] {
    record $run "checkpoint.next" {phase: $phase, name: $name, completedAt: (date now | format date "%+")}
    mv --force ($run | path join "checkpoint.next.json") ($run | path join "checkpoint.json")
    print --stderr $"\nCHECKPOINT: ($name)\n"
}

export def record [run: path, name: string, value: any] {
    $value | to json | save --force ($run | path join $"($name).json")
}
