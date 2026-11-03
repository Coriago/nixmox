# Run through direnv, or with: nix develop -c just test
test:
    go test ./... -count=1 -timeout=120s

# Backend foundation checks while the remaining runtime/UI integration is built.
test-backend:
    go test ./internal/identity ./internal/cluster ./internal/task ./internal/source ./internal/host ./internal/cache ./internal/credentials ./internal/client ./internal/cli ./internal/recipe ./internal/configuration ./internal/api ./internal/util -count=1 -timeout=120s

# Race checks include runtime ownership and backend integrations, not legacy UI.
test-backend-race:
    CGO_ENABLED=1 go test -race ./internal/identity ./internal/cluster ./internal/task ./internal/source ./internal/host ./internal/cache ./internal/credentials ./internal/client ./internal/cli ./internal/recipe ./internal/configuration ./internal/api ./internal/util ./internal/daemon -count=1 -timeout=120s

# Local mTLS cluster creation, recipe API/observations, and cold-restart integration.
test-runtime:
    go test ./internal/daemon -run TestClusterLifecycle -count=1 -v -timeout=90s

# Shared UI/component and HTTP workflow tests.
test-ui:
    go test ./internal/ui/... -count=1 -timeout=90s

# Real Nix build -> retained logs -> shared monitor projection.
test-build-monitor:
    NIXMOX_TEST_NIX=1 NIXMOX_TEST_NIXPKGS="$(nix eval --raw --impure --expr '(builtins.getFlake (toString ./.)).inputs.nixpkgs.outPath')" go test ./internal/host -run TestBuildMonitorPipeline -count=1 -v -timeout=90s

ui-assets:
    mkdir -p ui-assets
    GOOS=js GOARCH=wasm go build -trimpath -o ui-assets/app.wasm ./cmd/nixmox
    install -m644 "$(go env GOROOT)/lib/wasm/wasm_exec.js" ui-assets/wasm_exec.js

# Evaluate every architecture/profile/format, including image assertions.
check-installers:
    nix eval --json "path:{{justfile_directory()}}#packages" --apply 'systems: builtins.mapAttrs (_: packages: builtins.mapAttrs (_: package: package.drvPath) (builtins.removeAttrs packages ["default"])) systems'

# Discovery transport, unjoined/shared storage and HTTP reads; loopback-only test traffic.
test-discovery:
    go test ./internal/discovery -count=1 -v -timeout=30s

# Installer components and CLI/API/NATS integration; no kexec, reboot or disk writes.
test-installers:
    go test ./internal/installer/... ./internal/api ./internal/cli ./internal/task ./internal/daemon -count=1 -timeout=90s

# Native architecture; use installer, installer-vm or their -kexec variants.
build-installer package="installer-vm":
    nix build "path:{{justfile_directory()}}#{{package}}" --out-link result

# Independent phase 1 or chained phases. Resume with --resume RUN --after CHECKPOINT.
# Add --keep-vms to retain guests and collector on success or failure.
[positional-arguments]
e2e-phase1 *args:
    nu test/scripts/run.nu --through 1 "$@"

[positional-arguments]
e2e-phase2 *args:
    nu test/scripts/run.nu --through 2 "$@"

# Phase 3 inherits phases 1/2; VM3/quorum/kexec operations remain unimplemented.
[positional-arguments]
e2e-phase3 *args:
    nu test/scripts/run.nu --through 3 "$@"

e2e-stop run:
    nu test/scripts/stop.nu {{quote(run)}}

# These recipes do not start a cluster.
build:
    go build -o bin/nixmox ./cmd/nixmox

run *args:
    go run ./cmd/nixmox {{args}}

# Recipe import/sync/fork and independent configuration collect/upgrade/render.
test-configurations:
    go test ./internal/recipe ./internal/configuration ./internal/api -count=1 -timeout=60s

test-registry:
    NIXMOX_TEST_NIX=1 go test ./internal/configuration -run TestConfigurationRegistry -count=1 -v -timeout=60s

test-cache:
    NIXMOX_TEST_STORE_PATH="$(nix eval --raw --expr 'builtins.toFile "nixmox-cache-test" "nixmox cache integration\n"')" go test ./internal/cache -run TestSignedCache -count=1 -v -timeout=60s

test-logging:
    go test ./internal/util -run '^Example(Configure|Error)$' -count=1 -v

test-otel:
    go test ./internal/util -run '^ExampleStart$' -count=1 -v
