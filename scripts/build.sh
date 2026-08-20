#!/usr/bin/env bash
# Build the flatline single binary into bin/.
set -euo pipefail

cd "$(dirname "$0")/.."

mkdir -p bin
CGO_ENABLED=0 go build -o bin/flatline ./cmd/flatline

echo "built bin/flatline"