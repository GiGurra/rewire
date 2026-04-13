#!/usr/bin/env bash
# Verifies that Go still inlines rewire's wrapper for the tiny leaf
# functions in example/bar. If this ever stops being true, the tests in
# example/foo/inlining_test.go still exercise behavior, but we'd no
# longer be empirically proving "rewire's mock check survives
# aggressive inlining" — which is a claim worth defending explicitly.
#
# Run from the repo root:
#   scripts/check-inlining.sh

set -euo pipefail

cd "$(dirname "$0")/.."

# Ensure the rewire binary is up to date — we need the current rewriter
# to be what actually runs during the -gcflags build.
go install ./cmd/rewire/

# Use an isolated cache so we don't pollute (or read from) the main
# test cache. We want a clean build so every inlining decision prints.
CACHE_DIR="$(mktemp -d)"
trap 'rm -rf "$CACHE_DIR"' EXIT

OUTPUT="$(GOFLAGS='-toolexec=rewire' GOCACHE="$CACHE_DIR" \
    go build -gcflags='-m=2' ./example/foo/ 2>&1)"

required_lines=(
    "inlining call to bar.TinyDouble"
    "inlining call to bar.TinyAdd"
    "inlining call to bar._real_TinyDouble"
    "inlining call to bar._real_TinyAdd"
)

missing=()
for line in "${required_lines[@]}"; do
    if ! grep -q -- "$line" <<<"$OUTPUT"; then
        missing+=("$line")
    fi
done

if [[ ${#missing[@]} -gt 0 ]]; then
    echo "FAIL: the Go inliner is no longer inlining rewire's rewritten wrappers." >&2
    echo "Missing inlining decisions:" >&2
    for line in "${missing[@]}"; do
        echo "  - $line" >&2
    done
    echo >&2
    echo "Full gcflags=-m=2 output for example/foo:" >&2
    echo "$OUTPUT" >&2
    exit 1
fi

echo "OK: rewire's wrappers are being inlined at every expected call site."
for line in "${required_lines[@]}"; do
    count=$(grep -c -- "$line" <<<"$OUTPUT")
    printf "  %-50s (%d inlining sites)\n" "$line" "$count"
done
