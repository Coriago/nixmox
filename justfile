# Run through direnv, or with: nix develop -c just test
test:
    go test ./... -count=1 -timeout=120s

# Backend foundation checks while the remaining runtime/UI integration is built.
test-backend:
    go test ./internal/identity ./internal/cluster ./internal/task ./internal/source ./internal/host ./internal/cache ./internal/credentials ./internal/client ./internal/cli ./internal/recipe ./internal/api -count=1 -timeout=120s

# Local mTLS cluster creation, recipe API/observations, and cold-restart integration.
test-runtime:
    go test ./internal/daemon -run TestClusterLifecycle -count=1 -v -timeout=90s

# These recipes do not start a cluster.
build:
    go build -o bin/nixmox ./cmd/nixmox

run *args:
    go run ./cmd/nixmox {{args}}

smoke-unjoined: build
    #!/usr/bin/env bash
    set -euo pipefail
    state=$(mktemp -d)
    port=$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()')
    endpoint="http://127.0.0.1:$port"
    bin/nixmox daemon --state-dir "$state" --listen "127.0.0.1:$port" --advertise "$endpoint" >"$state/daemon.log" 2>&1 &
    pid=$!
    trap 'kill "$pid" 2>/dev/null || true; wait "$pid" 2>/dev/null || true; rm -rf "$state"' EXIT
    for attempt in {1..50}; do
        kill -0 "$pid"
        if bin/nixmox --endpoint "$endpoint" --json health >"$state/health.json" 2>/dev/null; then break; fi
        sleep 0.1
    done
    jq -e '.status == "online" and .checks.natsServer.status == "not-applicable"' "$state/health.json" >/dev/null
    echo "Unjoined daemon and CLI health check passed (no NATS server started)."

test-registry:
    NIXMOX_TEST_NIX=1 go test ./internal/source -run TestConfigurationRegistry -count=1 -v -timeout=60s

test-cache:
    NIXMOX_TEST_STORE_PATH="$(nix eval --raw --expr 'builtins.toFile "nixmox-cache-test" "nixmox cache integration\n"')" go test ./internal/cache -run TestSignedCache -count=1 -v -timeout=60s

test-logging:
    go test ./internal/util -run '^Example(Configure|Error)$' -count=1 -v

test-otel:
    go test ./internal/util -run '^ExampleStart$' -count=1 -v
