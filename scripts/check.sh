#!/usr/bin/env bash
# Run the quality gates: gofmt, go vet, go test.
set -euo pipefail

cd "$(dirname "$0")/.."

echo "== gofmt =="
out=$(gofmt -l .)
if [ -n "$out" ]; then
  echo "gofmt: files need formatting:"
  echo "$out"
  exit 1
fi

echo "== go vet =="
go vet ./...

echo "== go test =="
go test ./...

echo "all checks passed"