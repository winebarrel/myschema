#!/usr/bin/env bash
# Run `go tool deadcode` on the production binary's main and exit
# non-zero if it reports any unreachable funcs OR if the tool itself
# fails. `go tool deadcode` exits 0 even when reporting findings, so
# we have to check both the exit code and the captured output.
#
# `go mod download` runs first to warm the module + tool cache so a
# fresh runner's `go: downloading …` lines (emitted to stderr by
# `go tool` on first invocation) don't get folded into the captured
# output below and look like a finding.
set -eu

go mod download

rc=0
out=$(go tool deadcode ./cmd/myschema 2>&1) || rc=$?

if [ "$rc" -ne 0 ] || [ -n "$out" ]; then
    printf '%s\n' "$out"
    exit 1
fi
