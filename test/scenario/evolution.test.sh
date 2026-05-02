#!/usr/bin/env bash
# Schema-evolution scenario: walks a typical table through six stages,
# asserting at each that the next plan-then-apply makes drift go away.
# Exercises the most common ALTER TABLE shapes:
#   - CREATE TABLE (initial)
#   - ADD COLUMN (nullable, no DEFAULT — see TODO.md note about
#     `DEFAULT ''` round-trip drift)
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
run_step "06 DROP COLUMN + auto-index"    "DROP COLUMN"       "$DATA/06_drop_column.sql"
run_step_no_diff "07 idempotent re-apply" "$DATA/06_drop_column.sql"

summary
