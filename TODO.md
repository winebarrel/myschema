# TODO

Open items only. Done work is in `git log` / closed PRs.

**Out of scope** for myschema, with different reasons:
- *Triggers, stored procedures / functions, events.* Imperative,
  version-tagged code rather than declarative schema. Manage out of
  band, or use the planned `-- myschema:execute` directive.
- *Sequences.* MySQL has no sequence object; TiDB does, but myschema
  is MySQL-targeted. A TiDB profile could lift this later.

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
