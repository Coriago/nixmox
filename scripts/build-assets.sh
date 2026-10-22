#!/usr/bin/env bash
set -euo pipefail
GOOS=js GOARCH=wasm go build -trimpath -o internal/httpapi/assets/renderer.wasm ./cmd/nixmox-render
cp "$(go env GOROOT)/lib/wasm/wasm_exec.js" internal/httpapi/assets/wasm_exec.js
