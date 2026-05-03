# myschema — agent guide

## What this is

myschema is a declarative schema management tool for MySQL. The user
writes the desired schema as plain SQL (`CREATE TABLE` / `CREATE VIEW`
/ etc.); myschema reads the current state from MySQL's
`information_schema`, diffs the two, and emits — or applies — the DDL
that brings current → desired. Three subcommands: `plan` (preview the
DDL), `apply` (run it), `dump` (serialize the live schema as SQL).

The desired side is parsed with `vitess.io/vitess/go/vt/sqlparser`;
the catalog side is read with `database/sql` +
`github.com/go-sql-driver/mysql`.

## Build & test

```sh
make build           # go build ./cmd/myschema (outputs ./myschema)
make vet             # go vet ./...
make test            # go test -p 1 -v ./... (requires a reachable MySQL)
make test-unit       # parser + diff + model only, no network
make lint            # golangci-lint run
make fix             # golangci-lint run --fix
make test-scenario   # bash test/scenario/run.sh (CLI scenario suite)
make clean-schema    # drop + recreate the test database
make schema          # load Chinook (MIT), employees (CC BY-SA), sakila (BSD-ish)
                     # into separate databases for ad-hoc dump / plan testing
make schema-drop     # remove the three sample databases
```

Once `make schema` is loaded, point myschema at a sample DB:

```sh
MYSCHEMA_DSN='root@tcp(127.0.0.1:3306)/Chinook'   ./myschema dump
MYSCHEMA_DSN='root@tcp(127.0.0.1:3306)/employees' ./myschema dump
MYSCHEMA_DSN='root@tcp(127.0.0.1:3306)/sakila'    ./myschema dump
```

All three round-trip cleanly (`dump` → `plan` reports `-- No changes`).
Employees exercises view-on-view dependency ordering (`current_dept_emp`
references `dept_emp_latest_date`); sakila exercises GEOMETRY columns,
`SPATIAL INDEX`, ENUM defaults, and FK ON UPDATE CASCADE chains.

Tests against MySQL connect to the DSN in `MYSCHEMA_TEST_DSN`
(default `root@tcp(127.0.0.1:3306)/`) via `internal/testutil.ConnectDB`. If
MySQL is unreachable the test fails — start it first:

```sh
docker compose up -d
make test
```

`make test` runs with `-p 1` (sequential packages) because integration tests
share a single MySQL instance.

`make test-unit` is the safe fallback when no MySQL is running.

For forward-compat testing against MySQL 9.x, `compose.yaml` defines a
second `mysql9` service on port 3307. `docker compose up -d` brings
both services up; `make test-mysql9` then runs the suite against
3307 (overriding both `MYSQL_PORT` and `MYSCHEMA_TEST_DSN` explicitly
so a pre-set DSN in the caller's environment doesn't leak in):

```sh
docker compose up -d
make test-mysql9
```

`make clean-schema-mysql9` resets the `myschema_test` DB on the 9.x
instance the same way `clean-schema` does for 8.0.

## Project layout

| Path                  | Role |
|-----------------------|------|
| `cmd/myschema/`       | CLI entry point (kong) |
| `cmd/command/`        | One file per subcommand: `plan`, `apply`, `dump` |
| `parser/`             | SQL → `model.Table` using `vitess.io/vitess/go/vt/sqlparser` |
| `catalog/`            | `information_schema` → `model.Table` (via `database/sql` + `go-sql-driver/mysql`) |
| `model/`              | `Table`, `Column`, `Constraint`, `ForeignKey`, `Index` |
| `diff/`               | Compares two `model.Table` maps and emits DDL |
| `options.go`          | Global `Options`, `FilterOptions`, `DropPolicy`, `ObjectCount` |
| `client.go`           | `*sql.DB` factory bound to `Options` |
| `plan.go` / `apply.go` / `dump.go` | Top-level operations called by CLI |
| `diff_all.go`         | Glues parser + catalog + diff for plan/apply |

## Parser quirks worth knowing

vitess sits between an application and MySQL, so its grammar accepts
the full MySQL surface (including GEOMETRY / SPATIAL types) and pretty-
prints in the catalog-friendly form. The trade-offs and rough edges:

- vitess restores `select` lists in the fully-qualified
  back-tick-quoted form
  (`select \`db\`.\`t\`.\`c\` AS \`c\` from \`db\`.\`t\``). View diffs
  go through the AST visitors in `parser/view.go` to normalise both
  sides into the same shape so spurious diffs don't fire.
- `CREATE INDEX i ON t (...)` is parsed as `*sqlparser.AlterTable`
  with an `AddIndexDefinition` AlterOption (vitess collapses the
  standalone `CREATE INDEX` into an ALTER TABLE). `applyAlterTable`
  handles both shapes.
- CURRENT_TIMESTAMP / NOW / etc. are restored by vitess as
  `current_timestamp()` (with empty parens). The catalog stores them
  without parens, so `parser.normalizeDefaultExpr` strips them and
  upper-cases the keyword to match.
- Some string defaults arrive from
  `information_schema.COLUMNS.COLUMN_DEFAULT` as bareword values
  (`G` rather than `'G'` for an enum, `hello` rather than `'hello'`
  for a varchar). vitess parses such barewords as a column reference
  (`*sqlparser.ColName`) rather than a string literal, so the
  catalog-side round-trip later breaks. `catalog.normalizeColumnDefault`
  handles three cases:
  - **Non-empty, type-agnostic (the common path)** — parses
    `SELECT <def>`, and if the resulting expression is a `*ColName`
    wraps the value in single quotes. Anything else (Literal,
    NullVal, BoolVal, function calls, …) is already valid SQL and
    passes through.
  - **Empty string, type-aware** — vitess can't parse a `SELECT` with
    no expression, so the empty default takes a separate path: for
    string-shaped types listed in `columnTypeAllowsEmptyStringDefault`
    the bare empty string is rewritten to `''`; everything else
    passes through as-is.
  - **Fixed-width `BINARY(N)`, type-aware** — MySQL surfaces an empty
    `BINARY(N)` default as the literal sentinel `"0x"` (independent
    of N) rather than the bare empty string. When the type-name
    starts with `binary` and the value is `"0x"`, rewrite to `''`
    so the round-trip closes; see PR #34 / TODO history for the
    rationale.

## Coverage

**In scope (v1):**

- `CREATE TABLE` (columns, PK / UNIQUE / CHECK / FK, secondary indexes,
  inline column-level PK / UNIQUE / REFERENCES, AUTO_INCREMENT, DEFAULT,
  ON UPDATE, COMMENT, generated columns)
- `CREATE INDEX` and `ALTER TABLE … ADD CONSTRAINT` in desired-side SQL
- `CREATE VIEW` (with optional column-alias list) — emitted as
  `CREATE OR REPLACE VIEW` on apply
- Catalog reader: tables, columns, PRIMARY / secondary indexes (incl.
  prefix length, DESC, INVISIBLE), CHECK constraints, foreign keys, views
- Diff: CREATE / DROP TABLE, ADD / MODIFY / DROP COLUMN,
  ADD / DROP CONSTRAINT (PK / CHECK), ADD / DROP INDEX, ADD / DROP FK,
  CREATE OR REPLACE / DROP VIEW
- `--allow-drop` policy with `all,table,view,column,constraint,foreign_key,index`
- `--include` / `--exclude` glob filtering on table names
- CLI: `plan`, `apply`, `dump`
- `-- myschema:renamed-from <old>` directive on tables, columns, and
  secondary indexes. Statement-level on `CREATE TABLE` for table
  rename; inline (line above the column / index) inside the
  parenthesised body for column / index rename. Drives
  `ALTER TABLE … RENAME TO` (5.x+), `ALTER TABLE … RENAME COLUMN`
  (8.0+ only), and `ALTER TABLE … RENAME INDEX` (5.7+). The project
  baseline is MySQL 8.0 because of INVISIBLE indexes and CHECK
  constraints elsewhere in the codebase, so the 8.0-only RENAME
  COLUMN sits inside that same envelope; on a 5.x server only
  RENAME COLUMN would be rejected. Index parts referencing renamed
  columns are rewritten in place — including child-side FK Columns,
  parent-side cross-table FK RefCols, self-referential FK RefCols,
  and PRIMARY KEY constraint columns — so the index / FK / PK isn't
  also marked for DROP+CREATE. Functional / expression index parts
  are not rewritten (would need a real SQL expression parser); a
  rename that affects an expression index surfaces as DROP+CREATE
  on that one index. Errors fail the plan when the source object
  isn't present (typo'd old name shouldn't silently become a
  destructive DROP+CREATE); idempotent if the rename has already
  been applied. CHECK constraints and foreign keys also accept the
  directive as a typo guard: MySQL has no in-place RENAME CONSTRAINT
  / RENAME FOREIGN KEY, so the diff still emits DROP+ADD, but the
  directive is consumed at plan time so a source name that doesn't
  exist on the current side aborts the plan with `renamed-from:
  source … not found in current schema` instead of silently DROP+ADD'ing
  the wrong target.

**Operational rules** (declarative-by-design constraints; see
`CAVEATS.md` for the full list and rationale):

- Foreign keys must declare a covering index in desired SQL. myschema
  does not auto-skip the implicit covering index that MySQL creates
  for un-indexed FK columns, so a desired-side FK without an explicit
  matching index (PRIMARY KEY / UNIQUE / KEY) shows up as drift —
  surfaced as a `-- skipped: DROP INDEX` line in plan by default.
  Running apply with `--allow-drop=index` will then error 1553 because
  the FK still needs the index. `dump` always emits the covering
  index, so `dump → apply` is unaffected.

**Not yet implemented (intentional v1 cuts):**

(Triggers, stored procedures / functions, and events are deliberately
out of scope, not deferred — they are imperative, version-tagged code
rather than declarative schema. Manage them out of band.)
- View `WITH CHECK OPTION` fidelity: vitess's AST surfaces "no WITH
  clause" and "WITH CASCADED CHECK OPTION" indistinguishably (both
  arrive as the empty string), so the parser collapses both to
  `NONE`. Users who explicitly write `WITH CASCADED` see it dropped
  on round-trip. `WITH LOCAL CHECK OPTION` is preserved.
- View `DEFINER` and `SQL SECURITY` clauses are catalogued but not
  diffed; `CREATE OR REPLACE VIEW` uses MySQL's defaults.
- `ENUM` / `SET` column-type-level diffing (CompactStr renders them as text
  literals; equality works but rename/order isn't tracked)
- `-- myschema:execute` arbitrary-SQL escape hatch (the directive
  registry in `parser/directive.go` is already shaped for it; needs
  a parser pass, model bucket, and apply-time runner).
- Partition / sub-partition definitions
- Topological ordering of DDL when one new table FK-references another that
  is also being created in the same plan (currently the FK adds run after
  all CREATE TABLEs, so this works for that case; FKs that point at tables
  in databases myschema is not managing are not handled)
- Database-name remap (`-m old=new`): let the desired SQL use database
  `foo` while applying to database `bar`. Today the DSN's database is
  the single source of truth; if you point it at `bar`, every table
  reference in the desired SQL must also use `bar`.
- `--split` for `dump`, `--pre-sql` / `--concurrently-pre-sql`

When extending: prefer adding YAML-driven tests under a `testdata/`
tree over Go table tests when the scenario is purely SQL-input →
SQL-output.

## Development workflow

1. Create a feature branch before starting implementation.
2. Write a test that asserts the expected behaviour first, confirm it fails,
   then implement the fix / feature.
3. Prefer simplicity — avoid clever or layered implementations when a
   straightforward approach works. Match the scope of the change to what
   was actually requested; resist scope creep into adjacent cleanup.
4. After implementation:
   - Verify test cases are comprehensive (missing scenarios, edge cases).
   - Verify coverage has not decreased and cover any reachable paths that
     can be tested naturally. Do not write unnatural tests for unreachable
     defensive code.
   - Consider whether similar issues exist elsewhere in the codebase.
   - Run `make lint`.
   - With docker compose MySQL up, run `make test` and `make test-scenario`
     to confirm no regressions in the integration suites.
5. Do not run tests in parallel (`make test` uses `-p 1`) — the integration
   tests share a single MySQL instance.
6. When something looks wrong in catalog output, dump the raw rows from
   `information_schema` first; do not assume the parser side is wrong.

## Code conventions

- Package-level tests use **external** test packages (e.g. `package
  catalog_test`, `package model_test`). Use same-package tests only when
  access to unexported identifiers is required (e.g. `package diff` to
  hit `normalizeDef`).
- Root-level integration tests live in `package myschema_test`
  (`apply_test.go` / `plan_test.go` / `dump_test.go`) and consume YAML
  fixtures under `testdata/{apply,plan,dump}/*.yml`. Required fields vary
  per suite — the authoritative list is the `*TestCase` struct at the top
  of each `_test.go` (`applyTestCase` / `planTestCase` / `dumpTestCase`).
  Common shape: `init` seeds the test DB; `desired` is what the operation
  receives; the suite-specific assertion field is `applied` / `plan` /
  `dump`. Optional fields include `allow_drop`, `include`, `exclude`, and
  (apply-only) `verify_no_drift`.
- Add a YAML fixture whenever the test is purely SQL-input → SQL-output.
  Reach for a Go test only when the scenario can't be expressed that way
  (multi-database setups, error-path assertions on internal state, file-IO
  failures). When the harness lacks a field for a behaviour you want to
  assert in a fixture, **extend the `*TestCase` struct with one optional
  field** (defaulting to nil/zero so existing fixtures keep passing) rather
  than rewriting the test in Go.
- CLI scenario tests under `test/scenario/` cover end-to-end flows
  (plan → apply → drift check) that exercise the binary, not the library
  surface.
- `orderedmap.Map` is used throughout for deterministic iteration order of
  schema objects; reach for it any time iteration order matters for output.
- Identifiers go through `model.Ident`, which back-tick-quotes anything
  that isn't a safe `[a-zA-Z_][a-zA-Z0-9_$]*` token or that collides with
  a MySQL reserved word.
- Type names from both the parser and the catalog are lowercased
  before comparison so casing differences (`BIGINT` vs `bigint`)
  don't trigger spurious diffs. **Integer display widths are an
  asymmetry**: MySQL 8.0+ strips them from
  `information_schema.COLUMNS.COLUMN_TYPE` (so the catalog returns
  `int` even if the column was declared `INT(11)`), but vitess
  preserves whatever the user wrote (`int(11)`). myschema does not
  normalise either side, so writing `INT(11)` in desired SQL
  surfaces as drift on every plan; use the bare type name (`INT`,
  `BIGINT`, …). The exception is `ZEROFILL`, which MySQL itself
  keeps in `COLUMN_TYPE` (e.g. `int(5) unsigned zerofill`); writing
  it on the desired side is round-trip-safe as long as the implicit
  `UNSIGNED` is also written.
- Foreign keys live in `Table.ForeignKeys`, not in `Constraints`. The diff
  orders FK drops first, then table / column / index changes, then FK
  adds — never combine these phases.
- Index parts: prefix length is `*int` on both sides — vitess's
  `IndexColumn.Length` and the catalog's
  `information_schema.STATISTICS.SUB_PART` scan target, both `nil`
  when the user didn't specify one. Each loader dereferences when
  set; otherwise `model.IndexPart.Length` keeps its struct zero-value
  (`0`), so both sides compare equal at `0` for "no prefix". Index
  types: treat `""` and `"BTREE"` as equivalent (BTREE is the InnoDB
  default).
- The CHECK-constraint diff uses a deliberately loose normaliser
  (`strings.ToLower` + strip whitespace + strip backticks). Replace with a
  proper parser/restore pass when adding richer CHECK support.

## Smoke-test the binary

Without a database:

```sh
./myschema --help
./myschema plan --help
```

With a local MySQL — the DSN must include the target database (myschema
operates on exactly one database per invocation):

```sh
export MYSCHEMA_DSN='root@tcp(127.0.0.1:3306)/app'
./myschema dump > current.sql
./myschema plan desired.sql
./myschema apply --allow-drop=all desired.sql
```
