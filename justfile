# Run through direnv, or with: nix develop -c just test
test:
    go test ./... -count=1 -timeout=120s

test-logging:
    go test ./internal/util -run '^Example(Configure|Error)$' -count=1 -v

test-otel:
    go test ./internal/util -run '^ExampleStart$' -count=1 -v
