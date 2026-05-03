#!/usr/bin/env bash
# Schema-evolution scenario: walks a typical table through six stages,
# asserting at each that the next plan-then-apply makes drift go away.
# Exercises the most common ALTER TABLE shapes:
#   - CREATE TABLE (initial)
#   - ADD COLUMN (NOT NULL DEFAULT '' — also pins the catalog
#     empty-string default normalisation)
#   - ADD INDEX (secondary)
#   - ADD CONSTRAINT FOREIGN KEY (separate child table + FK)
#   - MODIFY COLUMN (widen VARCHAR)
#   - DROP COLUMN with column-attached index suppression
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "$SCRIPT_DIR/helper.sh"

DATA="$SCRIPT_DIR/testdata/evolution"

setup_db ""

run_step "01 initial CREATE TABLE"        "CREATE TABLE"      "$DATA/01_initial.sql"
run_step "02 ADD COLUMN"                  "ADD COLUMN"        "$DATA/02_add_column.sql"
run_step "03 ADD INDEX (secondary)"       "CREATE INDEX"      "$DATA/03_add_index.sql"
run_step "04 ADD CONSTRAINT FOREIGN KEY"  "ADD CONSTRAINT"    "$DATA/04_add_fk.sql"
run_step "05 MODIFY COLUMN (widen)"       "MODIFY COLUMN"     "$DATA/05_modify_column.sql"
# 06: dropping `display_name` removes its column-attached index
# (`users_display_name_idx`) automatically — myschema's diff
# suppresses the explicit DROP INDEX. Pre-apply plan must contain
# the column drop AND must NOT contain the index drop, otherwise a
# regression in suppression would still pass run_step (apply would
# error on the redundant DROP INDEX, but only at runtime).
assert_not_contains "06a column-attached index drop is suppressed" \
  "$MYSCHEMA" plan --allow-drop all "$DATA/06_drop_column.sql" \
  -- 'DROP INDEX users_display_name_idx'
run_step "06b DROP COLUMN + auto-index"   "DROP COLUMN"       "$DATA/06_drop_column.sql"
run_step_no_diff "07 idempotent re-apply" "$DATA/06_drop_column.sql"

summary
