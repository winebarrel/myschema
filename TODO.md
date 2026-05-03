# TODO

Open items only. Done work is in `git log` / closed PRs.

**Out of scope** for myschema, with different reasons:
- *Triggers, stored procedures / functions, events.* Imperative,
  version-tagged code rather than declarative schema. Manage out of
  band, or use the `-- myschema:execute` directive (see CAVEATS.md).
- *Sequences.* MySQL has no sequence object; TiDB does, but myschema
  is MySQL-targeted. A TiDB profile could lift this later.

## Medium — silent diffs / fidelity gaps

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

## Medium — object coverage

- [ ] **Partition diff generation beyond RANGE/LIST suffix add
      + order-preserving subset drop.** Suffix `ADD PARTITION`
      and order-preserving subset `DROP PARTITION` (head /
      middle / tail OK) for RANGE / LIST (including `RANGE
      COLUMNS` / `LIST COLUMNS`) already ship
      (`--allow-drop=partition` gates DROP); see
      CAVEATS.md "Partitioning". Still on the floor: mid-list
      value changes (`REORGANIZE PARTITION old1, old2 INTO
      (...)`), HASH / KEY count operations (`COALESCE PARTITION
      n` for shrinks, `ADD PARTITION PARTITIONS n` for grows),
      strategy / expression changes (`REMOVE PARTITIONING` + new
      `PARTITION BY`), and both directions of "one side has no
      partitioning" — first-time `PARTITION BY` against an
      unpartitioned table *and* `REMOVE PARTITIONING` against an
      already-partitioned one. SUBPARTITION is intentionally out
      of scope (CAVEATS.md notes both the desired-side parse
      error and the catalog-side guard).

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

## Low — tests / docs / release

- [ ] YAML harness extended to parser and diff (currently Go table tests).
- [ ] `README.md` — currently just `# myschema`.
- [ ] `getting-started.md`.
- [ ] `CHANGELOG.md`.
- [ ] Demo asciinema or gif (pistachio-style).
- [ ] `.goreleaser.yml`.
- [ ] Renovate / dependabot config.

