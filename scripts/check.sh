#!/usr/bin/env bash
# Run the quality gates: hidden sources, gofmt, go vet, go test.
set -euo pipefail

cd "$(dirname "$0")/.."

echo "== hidden sources =="
# A .gitignore pattern without a leading slash matches at every depth. That is
# how `coverage.*`, written for Go coverage profiles, also matched
# internal/api/coverage.go: the file built fine locally, git never saw it, and
# the commit that shipped its tests and its three call sites left HEAD unable
# to compile. gofmt/vet/test all stayed green, because they read the working
# tree, not the index.
#
# This step reads what git actually sees. A source file that exists on disk and
# is ignored is reported here rather than at the next clone.
if [ -d .git ]; then
  hidden=$(git ls-files --others --ignored --exclude-standard \
    | grep -E '\.(go|sql|py)$|^internal/' || true)
  if [ -n "$hidden" ]; then
    echo "these source files are on disk but .gitignore hides them from git:"
    echo "$hidden" | sed 's/^/  /'
    echo "committing would silently leave them out; narrow the pattern in .gitignore"
    exit 1
  fi
fi

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