# myschema — agent guide

## What this is

myschema is the MySQL counterpart to [pistachio](https://github.com/winebarrel/pistachio).
Same package layout (`parser/`, `catalog/`, `model/`, `diff/`, `cmd/`), same
CLI surface (`plan` / `apply` / `dump`), but for MySQL.

The desired schema is parsed with the TiDB SQL parser; the current state is
read from `information_schema`; the diff package emits the DDL that brings
current → desired.

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

## Why `vitess.io/vitess/go/vt/sqlparser` (and not pingcap/tidb/pkg/parser)

The bootstrap shipped with pingcap, since the user originally asked for the
TiDB parser. We migrated to vitess once `make schema` surfaced two real
gaps:

- **GEOMETRY / SPATIAL types** — pingcap reserves the type code (`mysql.
  TypeGeometry = 0xff`) but its grammar (`parser.y`) has no production that
  creates a column with the type. Sakila's `address.location GEOMETRY`
  could not be parsed, so its dump → plan round-trip never closed.
- **MySQL coverage philosophy** — pingcap aims to parse what TiDB *executes*
  (a subset of MySQL by design). vitess sits between application and MySQL,
  so it must parse anything MySQL accepts and pass the rest through. Its
  design contract is broader.

Same Apache 2.0 license. Binary went from ~21 MB → ~15 MB after the swap
(`go-sql-driver/mysql` + kong + orderedmap + parser, stripped). The
trade-off is that vitess has a slightly different API surface
(`SplitStatementToPieces` / `Walk(visit, node)` instead of
`Parse / Accept(visitor)`) and pretty-prints differently — see
`parser/view.go` for the AST visitors that normalise the catalog form
(`select \`db\`.\`t\`.\`c\` AS \`c\` from \`db\`.\`t\``) into the parser
form so view diffs stay quiet.

`CREATE INDEX i ON t (...)` is parsed as `*sqlparser.AlterTable` with an
`AddIndexDefinition` AlterOption (vitess collapses standalone CREATE INDEX
into ALTER TABLE). `applyAlterTable` handles both shapes.

CURRENT_TIMESTAMP / NOW / etc. are restored by vitess as
`current_timestamp()` (with empty parens). The catalog stores them
without parens, so `parser.normalizeDefaultExpr` strips them and
upper-cases the keyword to match.

ENUM / SET / CHAR / temporal defaults arrive from
`information_schema.COLUMNS.COLUMN_DEFAULT` as bareword values
(`G` rather than `'G'` for an enum). vitess can't parse the bareword
form, so `catalog.normalizeColumnDefault` wraps non-numeric, non-
expression defaults of these types in single quotes before handing
them off.

## Coverage vs. pistachio

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

**Not yet implemented (intentional v1 cuts; would mirror pistachio):**

(Triggers, stored procedures / functions, and events are deliberately
out of scope, not deferred — they are imperative, version-tagged code
rather than declarative schema. Manage them out of band.)
- View `WITH CHECK OPTION` fidelity: pingcap's AST cannot distinguish
  "no WITH clause" from "WITH CASCADED CHECK OPTION", so the parser
  collapses both to `NONE`. Users who explicitly write `WITH CASCADED`
  see it dropped on round-trip. `WITH LOCAL CHECK OPTION` is preserved.
- **FK-implicit covering indexes are not recognised as implicit.** When
  `ADD FOREIGN KEY` runs on columns with no covering index, MySQL
  silently creates one (named after the FK). `information_schema.
  STATISTICS` doesn't flag it as implicit, so the catalog reads it as
  an ordinary index — desired SQL that declares the FK without also
  declaring the index then shows false drift, and apply fails with
  `Error 1553` once `--allow-drop=index` lets the DROP through (the
  FK still needs the index). Two workarounds: (a) declare the covering
  index explicitly in desired SQL (this is what `dump` outputs, so
  `dump → apply` round-trips cleanly); or (b) leave the implicit index
  in place and accept the `-- skipped: DROP INDEX` line in plan output.
  The proper fix is a diff-side suppression rule mirroring MySQL's
  reuse: skip a DROP INDEX whose columns form the left-most prefix of
  a surviving FK and where no other surviving index covers that FK.
- View `DEFINER` and `SQL SECURITY` clauses are catalogued but not
  diffed; `CREATE OR REPLACE VIEW` uses MySQL's defaults.
- `ENUM` / `SET` column-type-level diffing (CompactStr renders them as text
  literals; equality works but rename/order isn't tracked)
- Renaming via directives (pistachio's `-- pist:renamed-from`)
- `-- pist:execute` arbitrary-SQL escape hatch
- Partition / sub-partition definitions
- Topological ordering of DDL when one new table FK-references another that
  is also being created in the same plan (currently the FK adds run after
  all CREATE TABLEs, so this works for that case; FKs that point at tables
  in databases myschema is not managing are not handled)
- Database-name remap (the MySQL analogue of pistachio's `--schema-map`):
  let the desired SQL use database `foo` while applying to database `bar`.
  Today the DSN's database is the single source of truth; if you point it
  at `bar`, every table reference in the desired SQL must also use `bar`.
- `--split` for `dump`, `--pre-sql` / `--concurrently-pre-sql`

When extending: prefer adding YAML-driven tests under a `testdata/` tree
(matching pistachio's pattern) over Go-table tests, once a real MySQL
fixture loader is added.

## Development workflow

Inherited from pistachio. Apply the same discipline here:

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

Inherited from pistachio. The MySQL-specific bits sit at the bottom.

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
- Type names from both the parser and the catalog are lowercased and
  stripped of integer display widths (via
  `types.TiDBStrictIntegerDisplayWidth = true`) so they compare equal
  between sides.
- Foreign keys live in `Table.ForeignKeys`, not in `Constraints`. The diff
  orders FK drops first, then table / column / index changes, then FK
  adds — never combine these phases.
- Index parts: pingcap parser uses `Length = -1` for "no prefix length";
  the catalog returns `0`. Normalise to `0` at parse time. Index types:
  treat `""` and `"BTREE"` as equivalent (BTREE is the InnoDB default).
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
