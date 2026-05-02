#!/usr/bin/env bash
# View lifecycle: CREATE → modify body (CREATE OR REPLACE) → DROP.
# Pins that views show up in plan output, that body changes drive a
# replace, and that the DROP VIEW path under --allow-drop=all works.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "$SCRIPT_DIR/helper.sh"

DATA="$SCRIPT_DIR/testdata/views"

setup_db ""

run_step "01 base table"           "CREATE TABLE"        "$DATA/01_initial.sql"
run_step "02 ADD VIEW"             "CREATE OR REPLACE VIEW" "$DATA/02_add_view.sql"
run_step "03 MODIFY VIEW body"     "CREATE OR REPLACE VIEW" "$DATA/03_modify_view.sql"
run_step "04 DROP VIEW"            "DROP VIEW"           "$DATA/04_drop_view.sql"
run_step_no_diff "05 idempotent re-apply" "$DATA/04_drop_view.sql"

summary
