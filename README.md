# myschema

Declarative schema management for MySQL. Write the desired schema
as plain SQL (`CREATE TABLE` / `CREATE VIEW` / etc.); myschema
reads the current state from `information_schema`, diffs the two,
and emits — or applies — the DDL that brings current → desired.

Subcommands: `plan` (preview the DDL), `apply` (run it), `dump`
(serialize the live schema as SQL).

## Requirements

- **MySQL 8.0+** — the catalog reader uses
  `information_schema.CHECK_CONSTRAINTS` (8.0.16+),
  `STATISTICS.IS_VISIBLE` (8.0+), and emits `ALTER TABLE … RENAME
  COLUMN` (8.0+). MySQL 5.7 is not supported.
- Go 1.26+ to build from source.

See `AGENTS.md` (developer guide), `CAVEATS.md` (operational rules
and known sharp edges), and `PARTITIONING.md` (partition-diff
scope and limits) for details.
