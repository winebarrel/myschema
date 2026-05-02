#!/usr/bin/env bash
# `--include` / `--exclude` filtering.
#
# Set up: three tables (users, sessions, logs) all exist in the DB.
# Desired SQL declares only `users`. Without filtering, the plan would
# include DROP TABLE for the other two; with filtering, only the
# matching tables participate in the diff.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "$SCRIPT_DIR/helper.sh"

DATA="$SCRIPT_DIR/testdata/filter"

setup_db "$DATA/init.sql"

# 01: --include matches only `users` → no diff against the desired
# (which also only declares users). Both sessions and logs must be
# absent from the plan.
assert_not_contains "01a --include=users hides sessions" \
  "$MYSCHEMA" plan --allow-drop all -I users "$DATA/desired.sql" \
  -- 'DROP TABLE myschema_test.sessions'

assert_not_contains "01b --include=users hides logs" \
  "$MYSCHEMA" plan --allow-drop all -I users "$DATA/desired.sql" \
  -- 'DROP TABLE myschema_test.logs'

# 02: --exclude='log*' (glob) — sessions still drops, logs is hidden.
assert_contains "02 --exclude=log* still drops sessions" \
  "$MYSCHEMA" plan --allow-drop all -E 'log*' "$DATA/desired.sql" \
  -- 'DROP TABLE myschema_test.sessions'

assert_not_contains "03 --exclude=log* hides logs" \
  "$MYSCHEMA" plan --allow-drop all -E 'log*' "$DATA/desired.sql" \
  -- 'DROP TABLE myschema_test.logs'

# 04: no filter — both tables drop.
assert_contains "04 no filter drops sessions" \
  "$MYSCHEMA" plan --allow-drop all "$DATA/desired.sql" \
  -- 'DROP TABLE myschema_test.sessions'

assert_contains "05 no filter drops logs" \
  "$MYSCHEMA" plan --allow-drop all "$DATA/desired.sql" \
  -- 'DROP TABLE myschema_test.logs'

summary
