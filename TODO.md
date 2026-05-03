# TODO

Open items only. Done work is in `git log` / closed PRs.

**Out of scope** for myschema, with different reasons:
- *Triggers, stored procedures / functions, events.* Imperative,
  version-tagged code rather than declarative schema. Manage out of
  band, or use the `-- myschema:execute` directive (see CAVEATS.md).
- *Sequences.* MySQL has no sequence object; TiDB does, but myschema
  is MySQL-targeted. A TiDB profile could lift this later.

## Medium — silent diffs / fidelity gaps

- [ ] **One-shot CONVERT TO CHARACTER SET path.** The basic
      table-level / column-level charset diff lands as a regular
      ALTER (`ALTER TABLE … DEFAULT CHARSET=…` + per-column
      `MODIFY COLUMN`) and converges in two applies for tables with
      pre-existing string columns — see CAVEATS.md "Changing DEFAULT
      CHARSET". A future directive (`-- myschema:convert-charset` or
      a `--convert-charset` flag) could opt into emitting a single
      `ALTER TABLE … CONVERT TO CHARACTER SET …` instead, which
      rewrites the stored bytes and converges in one apply. Trade-off:
      heavyweight (table rebuild) and column-level explicit charsets
      get clobbered by CONVERT TO and need their own follow-up MODIFY,
      so the directive needs to handle that ordering.
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

## Low — tests / docs / release

- [ ] YAML harness extended to parser and diff (currently Go table tests).
- [ ] `README.md` — currently just `# myschema`.
- [ ] `getting-started.md`.
- [ ] `CHANGELOG.md`.
- [ ] Demo asciinema or gif (pistachio-style).
- [ ] `.goreleaser.yml`.
- [ ] Renovate / dependabot config.

