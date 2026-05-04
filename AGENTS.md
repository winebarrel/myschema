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

## Requirements

**MySQL 8.0+ is required.** myschema's catalog reader assumes
features that don't exist on 5.7:

- `information_schema.CHECK_CONSTRAINTS` (8.0.16+) — without it,
  CHECK constraints round-trip empty.
- `information_schema.STATISTICS.IS_VISIBLE` (8.0+) — the catalog
  query references this column unconditionally; on 5.7 the index
  load fails.
- `ALTER TABLE … RENAME COLUMN` (8.0+) — emitted for the
  `-- myschema:renamed-from` directive on columns; rejected on 5.7.

CI runs against MySQL 8.0 (default) and MySQL 9.4 (forward-compat,
see `make test-mysql9`). Older versions are not tested; some
schemas may happen to work, but features that touch the gaps above
will fail.

## Build & test

```sh
make build           # go build ./cmd/myschema (outputs ./myschema)
make vet             # go vet ./...
make test            # go test -p 1 -v -coverpkg=./... ./... (requires a reachable MySQL)
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
share a single MySQL instance. Every test in the suite assumes MySQL is
reachable — there is no MySQL-free `test-unit` lane any more (model-level
unit tests still don't open a connection themselves, but they're run as
part of the same `go test ./...` invocation, so MySQL has to be up
either way).

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
  handles both shapes; **every other ALTER clause** (`ADD COLUMN`,
  `MODIFY COLUMN`, `DROP COLUMN`, `DROP INDEX`, `RENAME COLUMN`,
  partition ops, …) is silently skipped *when the target table is
  already in the desired model*, mirroring the top-level parser
  default that skips `CREATE TRIGGER` / `INSERT` / `SET` /
  non-directive comments / etc. An `ALTER TABLE` against a table
  that isn't declared in the desired SQL still fails fast with
  `ALTER TABLE on unknown table …` — only the unhandled-clause
  case is silent. (Comments shaped like
  `-- myschema:<name>` are validated by `ValidateDirectives` first
  and unknown / malformed shapes error out — they're the one
  exception to the skip rule.) Both skip paths exist so raw
  `mysqldump` output parses cleanly. See CAVEATS.md "Unmodelled SQL in desired-side
  files is silently skipped" — a `--strict` mode that turns these
  into errors was prototyped and considered out of scope for v1.
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
  prefix length, DESC, INVISIBLE), CHECK constraints, foreign keys, views,
  and partition clauses (round-trip via `SHOW CREATE TABLE`)
- Diff: CREATE / DROP TABLE, ADD / MODIFY / DROP COLUMN,
  ADD / DROP CONSTRAINT (PK / CHECK), ADD / DROP INDEX, ADD / DROP FK,
  CREATE OR REPLACE / DROP VIEW. Partition diffs (RANGE / LIST suffix
  add, subset drop, HASH/KEY count change, per-partition definition
  rewrite via REORGANIZE) live in `diff/partitions.go`; the full
  scope, plan-time validation errors, and unsupported shapes are
  in PARTITIONING.md.
- `--allow-drop` policy with `all,table,view,column,constraint,foreign_key,index,partition`
- `--include` / `--exclude` glob filtering on table names
- `--alter-algorithm` / `--alter-lock` flags (and matching
  `MYSCHEMA_ALTER_ALGORITHM` / `MYSCHEMA_ALTER_LOCK` env vars) to
  inject MySQL online-DDL hints into every generated `ALTER TABLE` and
  `CREATE INDEX`. myschema picks the right separator per DDL (comma
  for ALTER TABLE, space for CREATE INDEX) so the user only supplies
  the value (`INPLACE`, `NONE`, …); MySQL rejects unsupported
  combinations at apply time, so CI catches non-online migrations.
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
- `-- myschema:execute <check-sql>` directive — escape hatch for
  objects myschema doesn't model (triggers, routines, events,
  grants). The check SQL is run against the live database; zero
  rows back means "not applied yet, run the guarded statement",
  non-zero means skip. Idempotent across re-applies; runs after
  every other DDL bucket so the guarded SQL can refer to brand-
  new tables. See CAVEATS.md for the workflow and limits.
- `-- myschema:convert-charset` directive (statement-level, above
  a `CREATE TABLE`). Opts the next charset diff into a one-shot
  `ALTER TABLE … CONVERT TO CHARACTER SET <new> [COLLATE <new>]`
  instead of the default two-stage `DEFAULT CHARSET=…` +
  per-column MODIFY flow. The directive takes no arguments —
  the target charset / collation come from the CREATE TABLE's
  own DEFAULT CHARSET / COLLATE clauses; the parser errors at
  parse time when CREATE TABLE has no DEFAULT CHARSET. See
  CAVEATS.md "Changing DEFAULT CHARSET" for the trade-offs
  (heavyweight rebuild, column-level explicit charsets get
  clobbered).

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
- Changing `DEFAULT CHARSET` on a table that has pre-existing string
  columns converges in **two applies**, not one. The first apply just
  changes the table default; the second emits one `MODIFY COLUMN`
  per string column to inherit the new default. This is the honest
  expression of MySQL's own behaviour (`ALTER TABLE … DEFAULT
  CHARSET=…` does not rewrite per-column charset metadata). See
  CAVEATS.md for the full mechanics and one-shot workarounds.
- Emitted DDL is **unqualified** — `CREATE TABLE name`,
  `ALTER TABLE name …`, `CREATE OR REPLACE VIEW name AS …`,
  `DROP TABLE name`, `CREATE INDEX i ON name`, etc. myschema
  operates on exactly one database per invocation (the DSN carries
  it), so the qualifier would be noise on every line. The one
  exception is a foreign-key `REFERENCES` whose target lives in a
  different database (`fk.RefDB != fk.Database`): the
  cross-database qualifier is preserved so the FK doesn't silently
  re-target a same-named table in the current DB. (Cross-database
  FK *management* is intentionally out of scope — see CAVEATS.md
  "Foreign keys to tables in another database are passed through,
  not managed"; emission only has to not misinterpret one the user
  spelled out by hand.) Internal
  state — `model.{Table,View,ForeignKey}.Database`, `RefDB`, and
  the `FQTN` / `FQVN` map keys — stays db-qualified; the omission
  is purely about emitted DDL. View bodies (the SELECT clause) are
  also left as MySQL emits them via
  `information_schema.VIEWS.VIEW_DEFINITION`, which means
  fully-qualified `db.table.col` references survive in the body
  even though the view *name* in `CREATE OR REPLACE VIEW name`
  doesn't; `parser.NormalizeViewDefinition` strips both sides
  before diff comparison so the asymmetry doesn't fire.

**Not yet implemented (intentional v1 cuts):**

(Triggers, stored procedures / functions, and events are deliberately
out of scope, not deferred — they are imperative, version-tagged code
rather than declarative schema. Manage them out of band.)
- View `WITH CHECK OPTION` fidelity: vitess's AST surfaces "no WITH
  clause" and "WITH CASCADED CHECK OPTION" indistinguishably (both
  arrive as the empty string), so the parser collapses both to
  `NONE`. Users who explicitly write `WITH CASCADED` see it dropped
  on round-trip. `WITH LOCAL CHECK OPTION` is preserved.
- View `DEFINER` and `SQL SECURITY` clauses are intentionally out of
  scope. The catalog query no longer pulls `DEFINER` /
  `SECURITY_TYPE`, `model.View` no longer carries them, and the
  parser no longer reads them into the model — the diff has nothing
  to compare and `CreateSQL` emits neither clause. Reasons: vitess
  can't parse the canonical catalog `user@%` host quoting (so
  DEFINER can't round-trip), and emitting `SQL SECURITY DEFINER`
  (the MySQL default) on every view would noise up dump output
  without solving a real user need. Manage DEFINER / SECURITY
  changes by hand outside myschema. See CAVEATS.md.
- `ENUM` / `SET` column-type-level diffing (CompactStr renders them as text
  literals; equality works but rename/order isn't tracked)
- Partition diffs beyond the supported shapes (split / merge /
  reorder REORGANIZE, strategy / expression change, first-time
  `PARTITION BY` against an unpartitioned catalog table,
  `REMOVE PARTITIONING` against a partitioned one). v1 errors out
  on these so the user runs the ALTER by hand. See PARTITIONING.md
  for the full scope, the supported shapes, and the workarounds.
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
   then implement the fix / feature. **Every behaviour-changing edit must
   ship with a regression test in the same commit / PR — no exceptions.**
   This applies equally to first-pass implementation and to fix-ups made
   during PR review rounds: if a review comment causes you to change how
   the code behaves (not just rename a symbol or rewrap a comment), add or
   extend a test that would have failed before the fix. Picking up the
   reviewer's listed line without pinning the new behaviour is how
   regressions sneak back in.
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
  don't trigger spurious diffs. Integer display widths (`INT(11)`)
  *do* drift — desired-side parser keeps the width, catalog
  strips it on MySQL 8.0+. ZEROFILL is the exception (MySQL keeps
  the width). User-facing rules and rationale live in CAVEATS.md
  "Integer display widths drift; type-name casing doesn't".
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
