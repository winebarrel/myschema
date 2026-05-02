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
```

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
| `parser/`             | SQL → `model.Table` using `github.com/pingcap/tidb/pkg/parser` |
| `catalog/`            | `information_schema` → `model.Table` (via `database/sql` + `go-sql-driver/mysql`) |
| `model/`              | `Table`, `Column`, `Constraint`, `ForeignKey`, `Index` |
| `diff/`               | Compares two `model.Table` maps and emits DDL |
| `options.go`          | Global `Options`, `FilterOptions`, `DropPolicy`, `ObjectCount` |
| `client.go`           | `*sql.DB` factory bound to `Options` |
| `plan.go` / `apply.go` / `dump.go` | Top-level operations called by CLI |
| `diff_all.go`         | Glues parser + catalog + diff for plan/apply |

## Why `tidb/pkg/parser` and not `github.com/pingcap/parser`

The user originally asked for `github.com/pingcap/parser`. That standalone
module (`v3.1.2+incompatible`, last touched 2019/2020) requires
`github.com/pingcap/tidb/types/parser_driver` from a matching old TiDB
revision — wiring those module versions into a Go 1.26 project pulls in a
broken `tipb` / gRPC stack and effectively cannot build today.

`github.com/pingcap/tidb/pkg/parser` is the same parser, maintained inside the
TiDB monorepo since the v3.1 split; it ships its own value driver
(`pkg/parser/test_driver`) so no extra wiring is needed. We use it under that
import path. The semantics (AST, `Restore` API, `format.RestoreCtx`) are
identical to the standalone repo's API.

## Coverage vs. pistachio

**In scope (v1):**

- `CREATE TABLE` (columns, PK / UNIQUE / CHECK / FK, secondary indexes,
  inline column-level PK / UNIQUE / REFERENCES, AUTO_INCREMENT, DEFAULT,
  ON UPDATE, COMMENT, generated columns)
- `CREATE INDEX` and `ALTER TABLE … ADD CONSTRAINT` in desired-side SQL
- Catalog reader: tables, columns, PRIMARY / secondary indexes (incl.
  prefix length, DESC, INVISIBLE), CHECK constraints, foreign keys
- Diff: CREATE / DROP TABLE, ADD / MODIFY / DROP COLUMN,
  ADD / DROP CONSTRAINT (PK / CHECK), ADD / DROP INDEX, ADD / DROP FK
- `--allow-drop` policy with `all,table,column,constraint,foreign_key,index`
- `--include` / `--exclude` glob filtering on table names
- CLI: `plan`, `apply`, `dump`

**Not yet implemented (intentional v1 cuts; would mirror pistachio):**

- Views, triggers, routines (procedures / functions)
- `ENUM` / `SET` column-type-level diffing (CompactStr renders them as text
  literals; equality works but rename/order isn't tracked)
- Renaming via directives (pistachio's `-- pist:renamed-from`)
- `-- pist:execute` arbitrary-SQL escape hatch
- Partition / sub-partition definitions
- Topological ordering of DDL when one new table FK-references another that
  is also being created in the same plan (currently the FK adds run after
  all CREATE TABLEs, so this works for that case; cross-database ordering
  is not handled)
- `--with-tx` actually wraps in BEGIN / COMMIT — currently a no-op flag,
  documented as such, because MySQL auto-commits DDL anyway
- YAML-based testdata harness, scenario tests under `test/scenario/`
- Schema-name remap (`-m old=new`) — MySQL has no separate schemas / search
  paths, only databases; the equivalent would be database remap and is left
  for v2
- `--split` for `dump`, `--pre-sql` / `--concurrently-pre-sql`
- `dump_test.go`, `apply_test.go`, `plan_test.go` integration suites

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
- Root-level integration tests use `package myschema_test` once they exist.
- Test fixtures should land as YAML files in `testdata/` once the harness
  ships (TODO.md). Required fields will vary per suite (`apply` vs `plan`
  vs `dump`); the authoritative list will be the `*TestCase` struct at the
  top of each `_test.go` — when fixture YAML lacks a field you want to
  assert, prefer extending the `*TestCase` struct with one optional field
  (defaulting to nil/zero so existing fixtures keep passing) over writing
  the test in Go.
- Until the YAML harness lands, prefer Go table tests over scenario
  scripts for unit-level assertions. CLI scenario tests under
  `test/scenario/` are for end-to-end flows (plan → apply → drift check)
  that hit a real MySQL.
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

With a local MySQL (DSN defaults to `root@tcp(127.0.0.1:3306)/`):

```sh
./myschema -n app dump > current.sql
./myschema -n app plan desired.sql
./myschema -n app apply --allow-drop=all desired.sql
```
