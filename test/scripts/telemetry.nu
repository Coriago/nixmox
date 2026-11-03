# Standard collector process, owned by the suite rather than by an individual phase.
def attribute [attributes: list, name: string] {
    let found = ($attributes | where key == $name)
    if ($found | is-empty) { return null }
    $found.0.value.stringValue
}

def resources [run: path, signal: string, field: string] {
    mut collected = []
    for file in (glob ($run | path join $"telemetry/($signal)*.jsonl")) {
        for line in (open --raw $file | lines) {
            # A live exporter may be midway through its last line; retry it next poll.
            let batch = try { $line | from json } catch { null }
            if $batch != null { $collected = ($collected | append ($batch | get $field)) }
        }
    }
    $collected
}

export def wait-telemetry [run: path, node: string] {
    print --stderr $"\n=== Telemetry | Verify logs, server traces and host metrics for ($node) ===\n"

    for attempt in 0..29 {
        let logs = (resources $run logs resourceLogs)
        let startup = ($logs | where {|resource|
            $resource.scopeLogs | any {|scope|
                $scope.logRecords | any {|entry|
                    ($entry.body.stringValue? == "daemon listening") and ((attribute $entry.attributes node_id) == $node)
                }
            }
        })
        if ($startup | is-not-empty) {
            let host = (attribute $startup.0.resource.attributes host.id)
            let traces = (resources $run traces resourceSpans)
            let metrics = (resources $run metrics resourceMetrics)
            let traced = ($traces | any {|resource|
                ((attribute $resource.resource.attributes host.id) == $host) and ($resource.scopeSpans | any {|scope|
                    $scope.spans | any {|span| $span.name == "GET /health" and ($span.traceId | is-not-empty)}
                })
            })
            let measured = ([system.cpu.time system.memory.usage system.disk.io system.network.io] | all {|name|
                $metrics | any {|resource|
                    ((attribute $resource.resource.attributes host.id) == $host) and ($resource.scopeMetrics | any {|scope|
                        $name in $scope.metrics.name
                    })
                }
            })
            if $host != null and $traced and $measured {
                return {nodeId: $node, hostId: $host, logs: true, traces: true, hostMetrics: true}
            }
        }
        sleep 1sec
    }

    error make {msg: $"Central telemetry for ($node) is incomplete; inspect ($run)/telemetry and the daemon journal"}
}

export def wait-collector [run: path] {
    let directory = ($run | path join "telemetry")
    let session = (open --raw ($directory | path join "session.pid") | str trim)
    for attempt in 0..49 {
        let processes = (^ps -s $session -o args= | complete)
        if $processes.exit_code == 0 and ($processes.stdout | str contains ($directory | path join "collector.yaml")) {
            let probe = (^curl --fail --silent --max-time 1 http://127.0.0.1:14333 | complete)
            if $probe.exit_code == 0 { return }
        }
        sleep 200ms
    }

    error make {msg: $"Collector did not become healthy; see ($directory)/collector.log"}
}

export def start-collector [root: path, run: path] {
    print --stderr "\n=== Telemetry | Start the suite collector ===\n"

    let directory = ($run | path join "telemetry")
    mkdir $directory
    cp ($root | path join "test/otelcol.yaml") ($directory | path join "collector.yaml")

    do --capture-errors {
        ^otelcol-contrib validate --config ($directory | path join "collector.yaml")
        ^setsid --fork sh -c 'cd "$1"; echo $$ > session.pid; exec otelcol-contrib --config "$1/collector.yaml" > collector.log 2>&1 < /dev/null' sh $directory
    }

    # setsid --fork returns before its child creates the PID file.
    for attempt in 0..49 {
        if ($directory | path join "session.pid" | path exists) { break }
        sleep 100ms
    }
    wait-collector $run
}
