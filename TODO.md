# TODO

Tracking the gap between v1 (current) and feature parity with
[pistachio](https://github.com/winebarrel/pistachio). Items are roughly grouped
by area; ordering inside a group is not significant.

## Object coverage

- [x] Views (`CREATE VIEW` / `DROP VIEW`) — emitted as
      `CREATE OR REPLACE VIEW` on apply, diffed via
      `parser.NormalizeViewDefinition` (parse-then-restore via vitess plus
      AST visitors that strip schema/table qualifiers from column refs and
      drop redundant `SELECT col AS col` aliases).
- [ ] View `DEFINER` and `SQL SECURITY` diffing (catalogued but not acted on)
- [ ] Sequences (MySQL 8.0+ does not have native sequences; treat as out of
      scope unless TiDB compatibility is desired)
- [ ] Partitioning: `PARTITION BY` clause, sub-partitions, partition
      operations (`ADD/DROP/TRUNCATE/REORGANIZE PARTITION`)
- [ ] Generated column expression diff (currently the expression is captured
      but `equalGeneratedExpr` is a literal string compare)

> Triggers, stored procedures / functions, and events are intentionally
> out of scope: they are imperative, version-tagged code rather than
> declarative schema, and trying to diff them inside a schema-management
> tool tends to produce more confusion than value. Manage them out of
> band.

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
- [x] Topological ordering of **views** that reference other views.
      `parser.ViewReferences` collects every TableName ref via an AST
      visitor; `diff.topoSortViews` runs Kahn's algorithm with
      alphabetical tie-breaking. CreateStmts come out in dependency order,
      DropStmts in reverse. Verified by `make load-employees` →
      drop / re-apply → no drift.
- [ ] Cross-database FK ordering
- [ ] `DROP TABLE` ordering when one being-dropped table is referenced by an
      FK on another being-dropped table

## CLI features

- [ ] `--include` / `--exclude` glob already works for tables and views;
      extend to indexes / FKs
- [ ] `--enable` / `--disable` flag to restrict the object types
      considered (`table`, `view`), mirroring pistachio
- [ ] `--pre-sql` / `--pre-sql-file` (and `MYSCHEMA_PRE_SQL` env vars)
- [ ] `--split` for `dump` (one file per table / view)
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
      objects we do not model (triggers, routines, events, grants),
      with the pistachio "evaluate check-SQL during apply" semantics —
      the only sanctioned way to manage trigger/routine/event SQL
      alongside myschema-managed tables
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

- [x] YAML-driven test harness for plan, apply, dump
      (`testdata/{plan,apply,dump}/*.yml`; the parser / diff suites still
      use Go table tests). Extending to parser / diff is the remaining
      bullet.
- [ ] YAML harness for parser and diff
- [x] `apply_test.go` integration suite (YAML-driven, requires
      `MYSCHEMA_TEST_DSN` MySQL)
- [x] `dump_test.go` integration suite (YAML-driven)
- [x] `plan_test.go` integration suite (YAML-driven)
- [x] CLI scenario tests under `test/scenario/`
- [x] `compose.yaml` for a local MySQL 8.x test container

## Documentation

- [ ] `README.md` (currently only "# myschema" header from the bootstrap)
- [ ] `getting-started.md`
- [ ] `CHANGELOG.md`
- [ ] Demo asciinema or gif similar to pistachio

## Build / release

- [x] `Makefile` with `build`, `test`, `lint`, `fix`, `schema` targets
- [ ] `.golangci.yml`
- [ ] `.goreleaser.yml`
- [ ] CI workflow under `.github/workflows/ci.yml`
- [ ] Renovate / dependabot config

## Cleanup

- [ ] `applyAlterTable` accepts `ADD CONSTRAINT` only; either support more
      `ALTER TABLE` clauses in desired-side SQL or raise a clear error
- [ ] `parseOne` test helper in `diff/tables_test.go` is unused once the
      YAML harness lands
