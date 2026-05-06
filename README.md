# myschema

[![CI](https://github.com/winebarrel/myschema/actions/workflows/ci.yml/badge.svg)](https://github.com/winebarrel/myschema/actions/workflows/ci.yml)
[![codecov](https://codecov.io/github/winebarrel/myschema/graph/badge.svg?token=6dKYdrOiqP)](https://codecov.io/github/winebarrel/myschema)

Declarative schema management for MySQL. Write the desired schema
as plain SQL (`CREATE TABLE` / `CREATE VIEW` / etc.); myschema
reads the current state from `information_schema`, diffs the two,
and emits — or applies — the DDL that brings current → desired.

```sh
go install github.com/winebarrel/myschema/cmd/myschema@latest

export MYSCHEMA_DSN='root@tcp(127.0.0.1:3306)/app'
myschema dump > desired.sql       # capture current schema
$EDITOR desired.sql               # describe the schema you want
myschema plan desired.sql         # preview the DDL
myschema apply desired.sql        # run it
```

The `dump → edit → plan → apply → re-plan empty` round-trip is
the core workflow. See [`getting-started.md`](getting-started.md)
for a ten-minute walkthrough.

<img width="800" src="https://github.com/user-attachments/assets/87c6512b-2a2b-42e5-8774-4a85c4670c90" />

## Installation

### Homebrew

```bash
brew install winebarrel/myschema/myschema
```

### Download binary

Download the latest binary from [Releases](https://github.com/winebarrel/myschema/releases).

## Usage

Set the DSN once via env (or pass `--dsn=…` on every command):

```sh
export MYSCHEMA_DSN='root@tcp(127.0.0.1:3306)/app'
```

### `dump` — serialize the live schema

```sh
myschema dump > current.sql                          # full database
myschema dump --include 'user*' --exclude 'tmp_*'    # filter tables and views
myschema dump --split=./schema/                      # one SQL file per table/view
```

`dump` round-trips with `plan` reporting no changes — the output
is the canonical desired-side shape.

### `plan` — preview the DDL

```sh
myschema plan desired.sql                            # current → desired diff
myschema plan --allow-drop=column,index desired.sql  # allow specific drops
myschema plan --alter-algorithm=INPLACE --alter-lock=NONE desired.sql
myschema plan --bulk-alter desired.sql               # fold same-table ALTERs
```

Read-only; safe to run repeatedly. Drops blocked by the policy
appear as `-- skipped:` lines so nothing's hidden.

### `apply` — run the DDL

```sh
myschema apply desired.sql                           # safe ops only
myschema apply --allow-drop=all desired.sql          # destructive ops too
myschema apply --pre-sql 'SET FOREIGN_KEY_CHECKS=0;' desired.sql
```

`apply` runs whatever `plan` would print, in the same order. Use
`plan` first; `apply` has no separate confirmation prompt.

## Features

- Tables, columns (incl. INVISIBLE on MySQL 8.0+), primary /
  unique / check constraints, foreign keys, secondary indexes
  (with prefix length, DESC, INVISIBLE), generated columns,
  partitions (RANGE / LIST / HASH / KEY).
- Views (`CREATE OR REPLACE` on apply; cross-view dependency
  ordering).
- `--include` / `--exclude` glob filtering on table and view names.
- `--allow-drop=table,view,column,constraint,foreign_key,index,partition`
  — destructive ops are opt-in, per category. `all` enables every
  category at once.
- `--alter-algorithm=…` / `--alter-lock=…` — inject MySQL online-DDL
  hints into every generated `ALTER TABLE` / `CREATE INDEX`.
- `--bulk-alter` — fold consecutive same-table `ALTER TABLE`
  statements into one multi-spec ALTER.
- `--pre-sql` / `--pre-sql-file` — run session-level SQL on the
  connection before the diff (e.g. `SET FOREIGN_KEY_CHECKS=0`).
- `--split=<dir>` (dump only) — write one SQL file per table / view.
- Directives in desired SQL: `-- myschema:renamed-from`,
  `-- myschema:convert-charset`, `-- myschema:execute`.

Most flags have a matching `MYSCHEMA_*` env var (`--split` is
CLI-only).

## Requirements

- **MySQL 8.0+.** The catalog reader uses
  `information_schema.CHECK_CONSTRAINTS` (8.0.16+),
  `STATISTICS.IS_VISIBLE` (8.0+), and emits
  `ALTER TABLE … RENAME COLUMN` (8.0+). MySQL 5.7 isn't supported.
- Go 1.26+ to build from source.

## Documentation

- [`getting-started.md`](getting-started.md) — install, first DSN,
  dump → plan → apply round-trip, common flags, directives.
- [`AGENTS.md`](AGENTS.md) — developer guide: feature surface,
  parser quirks, diff invariants, layout. Read before sending a PR.
- [`CAVEATS.md`](CAVEATS.md) — operational rules, sharp edges,
  what myschema deliberately doesn't manage (triggers, routines,
  events, sequences).
- [`PARTITIONING.md`](PARTITIONING.md) — partition-diff scope and
  limits.
