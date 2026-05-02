#!/usr/bin/env bash
# Smoke scenario: round-trip a simple schema.
#   1. Empty DB → apply CREATE TABLE → assert plan now reports no changes.
#   2. Re-apply the same SQL → still no changes (idempotency).
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "$SCRIPT_DIR/helper.sh"

DATA="$SCRIPT_DIR/testdata/smoke"

setup_db ""

run_step "01 create users table" "CREATE TABLE" "$DATA/01_initial.sql"
run_step_no_diff "02 idempotent re-apply" "$DATA/01_initial.sql"

summary
