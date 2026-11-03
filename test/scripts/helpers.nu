# Transport and bounded waits only; domain CLI calls stay in phase1.nu.
export def guest [run: path, command: string, --port: int = 22221] {
    let result = (^ssh -F /dev/null -i ($run | path join "id_ed25519")
        -p $port -o BatchMode=yes -o ConnectTimeout=5
        -o StrictHostKeyChecking=accept-new
        -o $"UserKnownHostsFile=($run)/known_hosts"
        root@127.0.0.1 $command | complete)
    if $result.exit_code != 0 { error make {msg: $"guest command failed: ($result.stderr)"} }
    $result.stdout | str trim
}

export def wait-task [endpoint: string, id: string] {
    for attempt in 0..899 {
        print --stderr $"+ nixmox --endpoint ($endpoint) --json task show ($id)"
        let task = (^nixmox --endpoint $endpoint --json task show $id | from json | inspect)
        match $task.status {
            "succeeded" => { return $task }
            "manual-review" => { error make {msg: $"task ($id) unexpectedly requires approval"} }
            "failed" | "unknown" | "cancelled" => { error make {msg: ($task | to json)} }
        }
        if ($attempt mod 15) == 0 { print $"Task ($id): ($task.status)" }
        sleep 2sec
    }
    error make {msg: $"task ($id) exceeded 30 minutes"}
}

export def record [run: path, name: string, value: any] {
    $value | to json | save --force ($run | path join $"($name).json")
}
