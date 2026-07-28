#!/usr/bin/env bash
set -euo pipefail

# scripts/lint.sh - run the same checks as GitHub Actions locally
# Mirrors .github/workflows/go.yml's quality job.

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

echo "Installing linters if missing (golint, staticcheck)..."
if ! command -v golint >/dev/null 2>&1; then
  echo "golint not found; installing golang.org/x/lint/golint"
  go install golang.org/x/lint/golint@latest
fi
if ! command -v staticcheck >/dev/null 2>&1; then
  echo "staticcheck not found; installing honnef.co/go/tools/cmd/staticcheck"
  go install honnef.co/go/tools/cmd/staticcheck@latest
fi

echo "Checking gofmt..."
if [ -n "$(gofmt -l .)" ]; then
  echo "gofmt found formatting issues"
  gofmt -l .
  exit 1
fi

echo "Running go vet..."
go vet ./...

echo "Running golint..."
golint ./...

echo "Running staticcheck..."
staticcheck ./...

echo "Running go test..."
go test ./...

echo "Building CLI..."
go build -o gai .

echo "All local checks passed."
