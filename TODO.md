# TODO

Tracking the gap between v1 (current) and feature parity with
[pistachio](https://github.com/winebarrel/pistachio). Items are roughly grouped
by area; ordering inside a group is not significant.

## Object coverage

- [ ] Views (`CREATE VIEW`, `ALTER VIEW`, `DROP VIEW`) including
      `SQL SECURITY` / `DEFINER` / `WITH … CHECK OPTION`
- [ ] Triggers (`CREATE TRIGGER` / `DROP TRIGGER`)
- [ ] Stored procedures and functions (`CREATE PROCEDURE` / `CREATE FUNCTION`)
- [ ] Events (`CREATE EVENT`)
- [ ] Sequences (MySQL 8.0+ does not have native sequences; treat as out of
      scope unless TiDB compatibility is desired)
- [ ] Partitioning: `PARTITION BY` clause, sub-partitions, partition
      operations (`ADD/DROP/TRUNCATE/REORGANIZE PARTITION`)
- [ ] Generated column expression diff (currently the expression is captured
      but `equalGeneratedExpr` is a literal string compare)

## Column / type fidelity

- [ ] `ENUM` / `SET` element-list diff (parse the type, compare element order
      and whether values were appended vs. reordered)
- [ ] Default-value normalisation (parse → restore) so `'1'` vs `1`,
      `CURRENT_TIMESTAMP(6)` vs `current_timestamp(6)`, and the
      `(expression)` wrapper for non-literal defaults compare equal across
      parser side and `information_schema` side
- [ ] `CHECK` constraint definition normalisation. `diff.normalizeDef` is
      currently `lower + strip whitespace + strip backticks`; replace with
      a parse/restore pass so semantically equal CHECKs don't diff
- [ ] Character set / collation diff at the **table** and **column** levels
      (read currently, but not surfaced as `ALTER TABLE … CONVERT TO` or
      `MODIFY COLUMN … CHARACTER SET`)
- [ ] Column position (`AFTER`, `FIRST`) when adding new columns

## Diff ordering and safety

- [ ] Topological ordering of DDL when a new table FK-references another new
      table being created in the same plan (currently FK adds run after all
      `CREATE TABLE`s, which works for single-DB cases but is fragile)
- [ ] Cross-database FK ordering
- [ ] `DROP TABLE` ordering when one being-dropped table is referenced by an
      FK on another being-dropped table
- [ ] `--with-tx` actually wraps the apply in `BEGIN` / `COMMIT`. Today it
      is a no-op flag. MySQL auto-commits DDL, so the value is mostly for
      pre-SQL / arbitrary-SQL execution; document or remove

## CLI features

- [ ] `--include` / `--exclude` glob already works for tables; extend to
      indexes / FKs once view / trigger objects land
- [ ] `--enable` / `--disable` flag to restrict the object types
      considered (`table`, `view`, `trigger`, …), mirroring pistachio
- [ ] `--pre-sql` / `--pre-sql-file` (and `MYSCHEMA_PRE_SQL` env vars)
- [ ] `--split` for `dump` (one file per table / view / trigger)
- [ ] `--omit-database` for `dump` (mirror of pistachio's `--omit-schema`)
- [ ] Database-name remap (`-m old=new`), the MySQL analogue of pistachio's
      `--schema-map`. Useful when the dump target uses a different DB name
      from the source schema
- [ ] `dump --quote-style` (backtick vs. ANSI double-quote) so output can be
      consumed by tools that toggle `ANSI_QUOTES`

## Directive support (pistachio parity)

- [ ] `-- myschema:renamed-from <old_name>` directive on tables, columns,
      indexes, constraints, and FKs. Currently any rename is emitted as
      `DROP + CREATE`. Without directives, intent inference is brittle in
      MySQL because `information_schema` does not record rename history
- [ ] `-- myschema:execute <check-sql>` arbitrary-SQL escape hatch for
      objects we do not model (functions, triggers, grants), with the
      pistachio "evaluate check-SQL during apply" semantics
- [ ] `-- myschema:invisible` shortcut so an index can be added invisible
      first (lock-friendly) and made visible in a follow-up apply

## Drop policy

- [ ] Per-category `--allow-drop` flags already exist; surface
      "drops were suppressed" as a non-zero exit code option
      (`--strict-drop` or similar) for CI usage
- [ ] FK drops emitted because the owning table is being dropped should
      follow the table-drop policy (already implemented this way; add a
      regression test once the YAML harness lands)

## Tests / fixtures

- [ ] YAML-driven test harness for parser, diff, plan, apply, dump
      (mirror `pistachio/testdata/`)
- [ ] `apply_test.go` integration suite that runs against a real MySQL
      server, gated on `MYSCHEMA_TEST_DSN`
- [ ] `dump_test.go` integration suite (round-trip `CREATE TABLE` → dump →
      parse → diff is empty)
- [ ] CLI scenario tests under `test/scenario/`, similar to pistachio's
      shell-script suite
- [ ] `compose.yaml` for a local MySQL 8.x test container

## Documentation

- [ ] `README.md` (currently only "# myschema" header from the bootstrap)
- [ ] `getting-started.md`
- [ ] `CHANGELOG.md`
- [ ] Demo asciinema or gif similar to pistachio

## Build / release

- [ ] `Makefile` with `build`, `test`, `lint`, `fix`, `schema` targets
- [ ] `.golangci.yml`
- [ ] `.goreleaser.yml`
- [ ] CI workflow under `.github/workflows/ci.yml`
- [ ] Renovate / dependabot config

## Cleanup

- [ ] `--with-tx` flag: either implement or remove; current state is a
      misleading no-op
- [ ] `applyAlterTable` accepts `ADD CONSTRAINT` only; either support more
      `ALTER TABLE` clauses in desired-side SQL or raise a clear error
- [ ] `parseOne` test helper in `diff/tables_test.go` is unused once the
      YAML harness lands
