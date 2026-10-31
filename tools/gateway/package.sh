#!/usr/bin/env bash
# Creates a reviewable local deployment bundle; never installs or starts a server.
set -euo pipefail
repo="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)"
bundle="$repo/build/gateway"
mkdir -p "$bundle"
cd "$repo"
GOWORK=off CGO_ENABLED=0 go build -trimpath -o "$bundle/nyadb-gateway" ./cmd/nyadb-gateway
cp tools/gateway/gateway.example.json "$bundle/gateway.example.json"
cp tools/gateway/nyadb-gateway.service "$bundle/nyadb-gateway.service"
cp docs/transport-gateway.md "$bundle/README.md"
cd "$bundle"
sha256sum nyadb-gateway gateway.example.json nyadb-gateway.service README.md > SHA256SUMS
