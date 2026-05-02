#!/usr/bin/env bash
# `-- myschema:renamed-from` directive end-to-end. Walks one table
# through three renames (table → column → index), asserting at each
# step that the right ALTER … RENAME form lands in the plan and that
# post-apply plan reports no drift.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "$SCRIPT_DIR/helper.sh"

DATA="$SCRIPT_DIR/testdata/rename"

setup_db ""

run_step "01 initial CREATE TABLE"        "CREATE TABLE"   "$DATA/01_initial.sql"
run_step "02 RENAME TABLE"                "RENAME TO"      "$DATA/02_rename_table.sql"
run_step "03 RENAME COLUMN"               "RENAME COLUMN"  "$DATA/03_rename_column.sql"
run_step "04 RENAME INDEX (KEY + UNIQUE)" "RENAME INDEX"   "$DATA/04_rename_index.sql"
run_step_no_diff "05 idempotent re-apply" "$DATA/04_rename_index.sql"

summary
