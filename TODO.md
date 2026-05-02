# TODO

Open items only. Done work is in `git log` / closed PRs.

**Out of scope** for myschema, with different reasons:
- *Triggers, stored procedures / functions, events.* Imperative,
  version-tagged code rather than declarative schema. Manage out of
  band, or use the planned `-- myschema:execute` directive.
- *Sequences.* MySQL has no sequence object; TiDB does, but myschema
  is MySQL-targeted. A TiDB profile could lift this later.

## High — correctness bugs

- [ ] **FK-implicit covering indexes look like drift.** Adding a
      foreign key on un-indexed columns (inline `CREATE TABLE` or
      `ALTER TABLE … ADD CONSTRAINT … FOREIGN KEY` alike) silently
      creates an index named after the FK; `information_schema.STATISTICS`
      doesn't mark it as implicit. When desired SQL declares the FK
      without the covering
      index, the diff emits a `DROP INDEX` (suppressed unless
      `--allow-drop=index` is set), and `apply` then fails with
      `Error 1553`. Documented in AGENTS.md as a known limitation;
      not fixed yet. The proper fix is a diff-side suppression: skip
      a DROP INDEX whose columns form the left-most prefix of a
      surviving FK and where no other surviving index covers it
      (mirrors MySQL's reuse rule). Optional matching `dump` filter;
      safer to leave dump verbose.
- [ ] **`DROP TABLE` ordering when one being-dropped table is
      FK-referenced by another being-dropped table.** Currently the
      drop order is alphabetical by `TABLE_NAME`; if the parent comes
      first the apply errors. The `FKDropStmts → DropStmts` bucket
      separation handles the simple case but not all variants. Needs
      a topo-sort pass, mirror of the view-side fix.

## High — production gates

- [ ] **`-- myschema:renamed-from <old>` directive** on tables,
      columns, indexes, constraints, and FKs. Without it, every
      rename emits `DROP + CREATE` which destroys data. Pistachio's
      design is the reference.

## Medium — silent diffs / fidelity gaps

- [ ] **Character set / collation diff at the table and column level.**
      Currently read but not surfaced: a `CHARACTER SET` change is
      silently ignored. Needs care with table-default propagation
      (every column would otherwise look "different" after a table
      charset change).
- [ ] **`ENUM` / `SET` element-list diff.** Today the type is compared
      as one string, so the diff fires on any element-list change. ENUM
      ordering matters in MySQL (it backs the internal numeric mapping
      and ORDER BY result), so reordering is a real change — but
      *appending* a new value at the end is online-safe and could be
      surfaced as a hint or skipped from `--allow-drop` accounting.
- [ ] **FK to a table outside the managed database** — myschema only
      reads / orders the tables it manages, so an FK whose target lives
      in another database is treated as a black box. Apply will fail
      if the referenced table doesn't already exist; plan can't help.
- [ ] **Column position (`AFTER`, `FIRST`)** when adding new columns.

## Medium — object coverage

- [ ] **View `DEFINER` and `SQL SECURITY` diffing.** Catalogued but
      `DiffViews` doesn't act on changes.
- [ ] **Partitioning** — `PARTITION BY`, sub-partitions,
      `ADD/DROP/TRUNCATE/REORGANIZE PARTITION`.

## Low — CLI ergonomics

- [ ] `--enable` / `--disable` flag to scope a run to specific object
      types (`table`, `view`).
- [ ] `--include` / `--exclude` extension to indexes / FKs (currently
      tables and views only).
- [ ] `--pre-sql` / `--pre-sql-file` (and `MYSCHEMA_PRE_SQL` env vars).
- [ ] `--split` for `dump` (one file per table / view).
- [ ] `--omit-database` for `dump` (mirror of pistachio's `--omit-schema`).
- [ ] Database-name remap (`-m old=new`), the MySQL analogue of
      pistachio's `--schema-map`.
- [ ] `dump --quote-style` (backtick vs. ANSI double-quote) for tools
      that toggle `ANSI_QUOTES`.

## Low — directives

- [ ] **`-- myschema:execute <check-sql>`** arbitrary-SQL escape hatch
      for objects we don't model (triggers, routines, events, grants).
      The only sanctioned way to manage that SQL alongside
      myschema-managed tables.
- [ ] **`-- myschema:invisible`** shortcut so an index can be added
      invisible first (lock-friendly) and made visible in a follow-up
      apply.

## Low — tests / docs / release

- [ ] YAML harness extended to parser and diff (currently Go table tests).
- [ ] `README.md` — currently just `# myschema`.
- [ ] `getting-started.md`.
- [ ] `CHANGELOG.md`.
- [ ] Demo asciinema or gif (pistachio-style).
- [ ] `.goreleaser.yml`.
- [ ] Renovate / dependabot config.

## Cleanup

- [ ] `applyAlterTable` already handles `ADD CONSTRAINT` and
      `AddIndexDefinition` (the vitess shape for `CREATE INDEX`).
      Other `ALTER TABLE` clauses are silently ignored — raise a clear
      error so users learn that the desired state should be expressed
      via `CREATE TABLE`.
