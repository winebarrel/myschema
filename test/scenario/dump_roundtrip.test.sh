#!/usr/bin/env bash
# dump → apply round-trip: load a non-trivial schema, capture
# `myschema dump` output, drop the DB, re-apply the dump, then assert
# `myschema plan` against the original source SQL reports no changes.
# This pins that the dump renderer produces SQL the parser can read
# back without drift — covering tables, FKs, indexes, and views in a
# single pass.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "$SCRIPT_DIR/helper.sh"

DATA="$SCRIPT_DIR/testdata/dump_roundtrip"
# Portable: GNU `mktemp -d` accepts no template, BSD/macOS requires
# one. Pass an explicit template under $TMPDIR so both work.
TMP="$(mktemp -d "${TMPDIR:-/tmp}/myschema-roundtrip.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT

# 01: load the source schema directly via mysql (the source is hand-
# written DDL myschema can also apply, but we want to dump *that*
# state, not myschema's re-rendered version).
setup_db "$DATA/source.sql"

# 02: capture dump output.
step "01 dump current state"
dumped="$TMP/dump.sql"
if myschema_dump > "$dumped"; then
  pass
else
  fail "dump failed"
  cat "$dumped" >&2
  summary
  exit 1
fi

# 03: wipe and re-apply the dumped SQL. Allow drops since the wiped DB
# starts empty — there's nothing to drop, but --allow-drop=all keeps
# the apply path equivalent across runs.
setup_db ""
step "02 re-apply dumped SQL"
if "$MYSCHEMA" apply --allow-drop all "$dumped" >"$TMP/apply.log" 2>&1; then
  pass
else
  fail "apply failed"
  cat "$TMP/apply.log" >&2
  summary
  exit 1
fi

# 04: plan against the *original* source SQL — if the dump was
# faithful, there should be zero drift.
run_step_no_diff "03 plan against original source has no drift" "$DATA/source.sql"

summary
