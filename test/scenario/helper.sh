#!/usr/bin/env bash
# Common helpers for CLI scenario tests.

set -euo pipefail

: "${MYSCHEMA:=./myschema}"
: "${MYSCHEMA_TEST_DB:=myschema_test}"
# myschema reads its target DB from the DSN. If MYSCHEMA_TEST_DSN already
# contains a database we trust it; otherwise append MYSCHEMA_TEST_DB.
_base_dsn="${MYSCHEMA_TEST_DSN:-root@tcp(127.0.0.1:3306)/}"
case "$_base_dsn" in
  */)  export MYSCHEMA_DSN="${_base_dsn}${MYSCHEMA_TEST_DB}" ;;
  *)   export MYSCHEMA_DSN="$_base_dsn" ;;
esac

# `mysql` CLI invocation used to set up / tear down test databases.
# Override MYSQL_BIN if your client is not on $PATH (e.g. mysql-shell).
: "${MYSQL_BIN:=mysql}"
: "${MYSQL_HOST:=127.0.0.1}"
: "${MYSQL_PORT:=3306}"
: "${MYSQL_USER:=root}"
_mysql_args=(--protocol=TCP -h "$MYSQL_HOST" -P "$MYSQL_PORT" -u "$MYSQL_USER")

_pass=0
_fail=0
_current_step=""

step() {
  _current_step="$1"
  printf "  %-50s " "$1"
}

pass() {
  _pass=$((_pass + 1))
  echo "PASS"
}

fail() {
  _fail=$((_fail + 1))
  echo "FAIL"
  if [ $# -gt 0 ]; then
    echo "    $1" >&2
  fi
}

summary() {
  echo ""
  echo "  ${_pass} passed, ${_fail} failed"
  [ "$_fail" -eq 0 ]
}

# Reset the database and optionally run init SQL from a file.
setup_db() {
  "$MYSQL_BIN" "${_mysql_args[@]}" -e "DROP DATABASE IF EXISTS \`${MYSCHEMA_TEST_DB}\`; CREATE DATABASE \`${MYSCHEMA_TEST_DB}\`"
  if [ $# -gt 0 ] && [ -n "$1" ]; then
    "$MYSQL_BIN" "${_mysql_args[@]}" "${MYSCHEMA_TEST_DB}" < "$1"
  fi
}

# Run myschema plan against the test database.
myschema_plan() {
  "$MYSCHEMA" plan --allow-drop all "$@" 2>&1
}

# Run myschema apply against the test database.
myschema_apply() {
  "$MYSCHEMA" apply --allow-drop all "$@" 2>&1
}

# Run plan without --allow-drop (drops should be suppressed).
myschema_plan_no_drop() {
  "$MYSCHEMA" plan "$@" 2>&1
}

# Run a step that expects the plan output to contain a particular substring,
# applies it, and asserts that the second plan reports "No changes".
run_step() {
  local step_name="$1"
  local expected="$2"
  shift 2
  local files=("$@")

  step "$step_name"

  local plan_output
  plan_output=$(myschema_plan "${files[@]}") || { fail "plan failed: $plan_output"; return 1; }

  if ! echo "$plan_output" | grep -qF "$expected"; then
    fail "unexpected plan output"
    echo "    expected to contain: $expected" >&2
    echo "    actual: $plan_output" >&2
    return 1
  fi

  local apply_output
  apply_output=$(myschema_apply "${files[@]}") || { fail "apply failed: $apply_output"; return 1; }

  local drift
  drift=$(myschema_plan "${files[@]}") || { fail "post-apply plan failed: $drift"; return 1; }
  if ! echo "$drift" | grep -q 'No changes'; then
    fail "drift after apply"
    echo "    $drift" >&2
    return 1
  fi

  pass
}

# Run a step that should produce no diff at all.
run_step_no_diff() {
  local step_name="$1"
  shift
  local files=("$@")

  step "$step_name"

  local plan_output
  plan_output=$(myschema_plan "${files[@]}") || { fail "plan failed: $plan_output"; return 1; }
  if ! echo "$plan_output" | grep -q 'No changes'; then
    fail "expected no diff"
    echo "    $plan_output" >&2
    return 1
  fi
  pass
}
