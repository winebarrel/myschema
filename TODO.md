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
- [ ] **CHECK constraint `NOT ENFORCED` not preserved.** The
      `Enforced` flag is already plumbed end-to-end on the desired side
      (`model.Constraint.Enforced`, `diff.constraintEqual` compares it),
      but `catalog.loadCheckConstraints` hard-codes `Enforced: true`
      instead of selecting `information_schema.TABLE_CONSTRAINTS.ENFORCED`
      (which the catalog query already joins to `CHECK_CONSTRAINTS` for
      the table-name lookup). Result: a desired-side `CHECK (...) NOT
      ENFORCED` applies once, then every subsequent plan emits `DROP
      CHECK chk + ADD CONSTRAINT chk CHECK (...) NOT ENFORCED` —
      perpetual drift loop. Fix: select `tc.ENFORCED` and feed it into
      `model.Constraint.Enforced`; verify `addConstraintSQL` emits the
      suffix when `Enforced=false`. Surfaced while writing the
      regression-coverage fixtures (PR #58); fixture withheld until the
      catalog reader learns the flag or CAVEATS.md documents the
      limitation explicitly.
- [ ] **`TIMESTAMP NULL DEFAULT NULL` round-trip drifts.** Same shape:
      `verify_no_drift` fails because re-plan repeatedly emits
      `MODIFY COLUMN ts timestamp DEFAULT null` even though the
      column already has that default. Likely a normalisation gap
      between `information_schema.COLUMNS.COLUMN_DEFAULT` (which
      returns the default in a form the parser doesn't fold back to
      the same expression) and the desired-side AST. Surfaced
      while writing the regression-coverage fixtures (PR #58); same
      treatment as the `NOT ENFORCED` gap above.
- [ ] **Table-level `COMMENT='…'` changes are silent.** `model.Table.Comment`
      and the catalog's `TABLE_COMMENT` read into it are already wired
      up; the gap is in the diff layer — `diff/tables.go` (around the
      table-level CHARACTER SET / COLLATE branch) explicitly notes
      "Engine and Comment are intentionally not diffed here yet — out
      of scope for the charset gap", so changing the table comment in
      desired SQL produces no diff at all. Fix: add a Comment-diff
      branch that emits `ALTER TABLE … COMMENT='…'`. Surfaced while
      writing the regression-coverage fixtures (PR #58). Implement or
      document as out-of-scope in CAVEATS.md.

## Low — CLI ergonomics

- [ ] `--pre-sql` / `--pre-sql-file` (and `MYSCHEMA_PRE_SQL` env vars).
- [ ] `--split` for `dump` (one file per table / view).
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

