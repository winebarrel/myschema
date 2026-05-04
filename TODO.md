# TODO

Open items only. Done work is in `git log` / closed PRs.
For object families that are intentionally **not** on the roadmap
(triggers / stored procedures / functions / events / sequences),
see `CAVEATS.md` → "What myschema deliberately doesn't manage".

## Medium — silent diffs / fidelity gaps

- **CHECK constraint `NOT ENFORCED` not preserved.** The
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
- **`TIMESTAMP NULL DEFAULT NULL` round-trip drifts.** Same shape:
  `verify_no_drift` fails because re-plan repeatedly emits
  `MODIFY COLUMN ts timestamp DEFAULT null` even though the
  column already has that default. Likely a normalisation gap
  between `information_schema.COLUMNS.COLUMN_DEFAULT` (which
  returns the default in a form the parser doesn't fold back to
  the same expression) and the desired-side AST. Surfaced
  while writing the regression-coverage fixtures (PR #58); same
  treatment as the `NOT ENFORCED` gap above.
- **View column-alias list (`CREATE VIEW v (a, b) AS …`) changes
  are silent.** `model.View.Cols` is populated on the parser side
  but the catalog reader doesn't fill it: `catalog/views.go` queries
  only `information_schema.VIEWS`, whose `VIEW_DEFINITION` rewrites
  any user-supplied alias list into per-expression `AS` aliases
  inside the SELECT body and does not surface the original list.
  The user-facing names live in `information_schema.COLUMNS` keyed
  by view+ordinal position. Result: `viewEqual` deliberately skips
  `Cols` in the diff (PR #66 had to revert that comparison after
  CI showed every alias-list view drifting on every plan). Fix:
  teach the catalog reader to populate `Cols` from
  `information_schema.COLUMNS` for view rows, then re-add the
  `slices.Equal(a.Cols, b.Cols)` check in `diff/views.go`'s
  `viewEqual`.

## Low — CLI ergonomics

- `--pre-sql` / `--pre-sql-file` (and `MYSCHEMA_PRE_SQL` env vars).
- `--split` for `dump` (one file per table / view).

## Low — tests / docs / release

- YAML harness extended to parser and diff (currently Go table tests).
- `README.md` — currently just `# myschema`.
- `getting-started.md`.
- `CHANGELOG.md`.
- Demo asciinema or gif (pistachio-style).
- `.goreleaser.yml`.
- Renovate / dependabot config.
