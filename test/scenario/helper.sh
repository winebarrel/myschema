#!/usr/bin/env bash
# Common helpers for CLI scenario tests.

set -euo pipefail

: "${MYSCHEMA:=./myschema}"
: "${MYSCHEMA_TEST_DB:=myschema_test}"
# `mysql` CLI invocation used to set up / tear down test databases.
# Override MYSQL_BIN if your client is not on $PATH (e.g. mysql-shell).
: "${MYSQL_BIN:=mysql}"
: "${MYSQL_HOST:=127.0.0.1}"
: "${MYSQL_PORT:=3306}"
: "${MYSQL_USER:=root}"
_mysql_args=(--protocol=TCP -h "$MYSQL_HOST" -P "$MYSQL_PORT" -u "$MYSQL_USER")

# myschema reads its target DB from the DSN. If MYSCHEMA_TEST_DSN already
# contains a database we trust it; otherwise append MYSCHEMA_TEST_DB. The
# default is built from MYSQL_USER / MYSQL_HOST / MYSQL_PORT so the CLI
# (mysql) and myschema (driver) always hit the same instance — without
# this, overriding MYSQL_PORT alone (e.g. to 3307 for the 9.x leg) would
# leave myschema talking to 3306.
_base_dsn="${MYSCHEMA_TEST_DSN:-${MYSQL_USER}@tcp(${MYSQL_HOST}:${MYSQL_PORT})/}"
case "$_base_dsn" in
  */)  export MYSCHEMA_DSN="${_base_dsn}${MYSCHEMA_TEST_DB}" ;;
  *)   export MYSCHEMA_DSN="$_base_dsn" ;;
esac

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

# Run myschema dump against the test database. Unlike the plan / apply
# wrappers, this does NOT merge stderr into stdout — callers typically
# redirect stdout to a .sql file, and any error / warning text on
# stderr would otherwise end up embedded in the dump and break the
# re-apply round-trip.
myschema_dump() {
  "$MYSCHEMA" dump "$@"
}

# Assert that a step's command output (stdout + stderr) contains an
# expected substring.
# Usage: assert_contains <step-name> <command...> -- <expected substring>
# Example:
#   assert_contains "table is filtered" \
#     "$MYSCHEMA" plan -I 'users*' "$DATA/schema.sql" \
#     -- 'CREATE TABLE myschema_test.users'
#
# The "--" separator is required so the helper can find the boundary
# between cmd args and the expected substring; missing separator,
# missing command, or missing substring all fail loudly with a clear
# message rather than letting bash run a malformed command.
assert_contains() {
  local step_name="$1"
  shift
  step "$step_name"

  local cmd=()
  local found_sep=0
  while [ $# -gt 0 ]; do
    if [ "$1" = "--" ]; then
      found_sep=1
      shift
      break
    fi
    cmd+=("$1")
    shift
  done
  if [ "$found_sep" -ne 1 ]; then
    fail "assert_contains: missing '--' separator (cmd... -- substring)"
    return 1
  fi
  if [ ${#cmd[@]} -eq 0 ]; then
    fail "assert_contains: no command before '--'"
    return 1
  fi
  if [ $# -eq 0 ]; then
    fail "assert_contains: no expected substring after '--'"
    return 1
  fi
  if [ $# -ne 1 ]; then
    fail "assert_contains: too many arguments after '--' (got $#); quote the substring"
    return 1
  fi
  local expected="$1"

  local out
  out=$("${cmd[@]}" 2>&1) || { fail "command failed: $out"; return 1; }
  if echo "$out" | grep -qF "$expected"; then
    pass
  else
    fail "expected substring not found"
    echo "    expected: $expected" >&2
    echo "    actual:   $out" >&2
    return 1
  fi
}

# Same shape as assert_contains, but the substring must NOT appear.
assert_not_contains() {
  local step_name="$1"
  shift
  step "$step_name"

  local cmd=()
  local found_sep=0
  while [ $# -gt 0 ]; do
    if [ "$1" = "--" ]; then
      found_sep=1
      shift
      break
    fi
    cmd+=("$1")
    shift
  done
  if [ "$found_sep" -ne 1 ]; then
    fail "assert_not_contains: missing '--' separator (cmd... -- substring)"
    return 1
  fi
  if [ ${#cmd[@]} -eq 0 ]; then
    fail "assert_not_contains: no command before '--'"
    return 1
  fi
  if [ $# -eq 0 ]; then
    fail "assert_not_contains: no expected substring after '--'"
    return 1
  fi
  if [ $# -ne 1 ]; then
    fail "assert_not_contains: too many arguments after '--' (got $#); quote the substring"
    return 1
  fi
  local unexpected="$1"

  local out
  out=$("${cmd[@]}" 2>&1) || { fail "command failed: $out"; return 1; }
  if echo "$out" | grep -qF "$unexpected"; then
    fail "unexpected substring present"
    echo "    unexpected: $unexpected" >&2
    echo "    actual:     $out" >&2
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
