# TODO

Open items only. Done work is in `git log` / closed PRs.
For object families that are intentionally **not** on the roadmap
(triggers / stored procedures / functions / events / sequences),
see `CAVEATS.md` → "What myschema deliberately doesn't manage".

## Medium — silent diffs / fidelity gaps

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

- **`SRID <n>` on spatial columns is silently dropped.** vitess
  parses it into `cd.Type.Options.SRID` (a `*Literal`) but
  `parseColumnDef` doesn't read it. AGENTS.md "In scope (v1)"
  lists GEOMETRY columns and SPATIAL INDEX (sakila exercises
  both), but the SRID attribute on the column is lost on
  round-trip — and the catalog *does* surface SRID via
  `information_schema.ST_GEOMETRY_COLUMNS`, so a desired-side
  schema with `SRID 4326` constants drifts forever. Fix: add
  SRID to `model.Column`, emit in `columnDefSQL`, populate from
  the spatial catalog view. Found during the PR #81 audit.

- **Table-level storage / encryption options are silently
  dropped.** `applyTableOption` only handles `ENGINE`, `CHARSET`,
  `COLLATE`, `COMMENT`, `AUTO_INCREMENT`. Real production
  attributes that affect on-disk layout / encryption posture
  silently drop on the desired side and continue to drift on
  every plan:
  - `ROW_FORMAT={COMPACT|DYNAMIC|COMPRESSED|REDUNDANT}` —
    affects InnoDB on-disk layout.
  - `KEY_BLOCK_SIZE=N` — page size for compressed InnoDB tables.
  - `COMPRESSION='ZLIB|LZ4|...'` — InnoDB transparent
    compression (8.0+).
  - `ENCRYPTION='Y|N'` — InnoDB transparent encryption (8.0+).
  Decide which are in scope, then add them to `model.Table`,
  `applyTableOption`, the catalog reader, and `Table.SQL()`.
  `ROW_FORMAT` and `ENCRYPTION` are the most operationally
  significant. Found during the PR #81 audit.

- **Index-level `KEY_BLOCK_SIZE=N` is silently dropped.** vitess
  parses it but myschema's index loader (`addIndex`) doesn't
  read it, and `Index.SQL()` doesn't emit it. Same shape as the
  table-level item above but for compressed indexes. Found
  during the PR #81 audit.

- **Inline column-level `REFERENCES` auto-names the FK as
  `<table>_ibfk_<col>` but MySQL auto-names it `<table>_ibfk_<n>`
  (numeric).** Parser-side `posts_ibfk_user_id` vs catalog-side
  `posts_ibfk_1` for `user_id BIGINT REFERENCES users(id)` —
  every plan emits a redundant `ALTER TABLE posts ADD CONSTRAINT
  posts_ibfk_user_id …`. Workaround: write the FK explicitly as
  `CONSTRAINT fk_<name> FOREIGN KEY (...) REFERENCES …` and the
  names align. Fix options: (a) parser auto-naming matches
  MySQL's numeric scheme (fragile — need next free number), (b)
  catalog reader recognises the `_ibfk_<n>` pattern and rewrites
  to match parser's column-shaped naming, (c) docs recommend
  always-explicit FK names. Found during the kitchen-sink fixture
  work for the test-coverage audit.

- **Generated-column expression drifts when the body references a
  vitess-keyword identifier.** `GENERATED ALWAYS AS (CONCAT(email,
  ' ', name)) STORED` round-trips as
  ``CONCAT(email, ' ', `name`)`` because vitess `String()` on a
  `ColName` back-ticks any identifier in its keyword list (`name`
  is one). Catalog stores the back-ticked form via SHOW CREATE
  TABLE, parser doesn't, so re-plan emits a no-op `MODIFY COLUMN`
  every time. Workaround: rename the column to a non-keyword
  identifier. Fix: normalise expression bodies through one
  formatter on both sides (or strip uniformly-safe back-ticks
  before compare). Found during the kitchen-sink fixture work.

- **CHECK constraint referencing a renamed column blocks the
  rename.** When desired changes a column with a `--
  myschema:renamed-from` directive AND the catalog has a CHECK
  constraint referencing that column, the diff emits:
  1. RENAME COLUMN (from Stmts bucket via applyColumnRenames)
  2. DROP CHECK (later in Stmts via diffConstraints)
  3. ADD CONSTRAINT (replacement CHECK)
  MySQL 8.0+ rejects step 1 with `Error 3959 Check constraint 'X'
  uses column 'Y', hence column cannot be dropped or renamed`
  because the CHECK is still active when the rename runs. Fix:
  either (a) introduce a dedicated CHECK-drop bucket that runs
  before column renames (mirroring the FK-drop bucket which
  already handles this), or (b) defer the rename until after
  CHECK drops within the same `Stmts` slice. Discovered while
  composing the inline_rename_all_kinds.yml fixture; the fixture
  works around it by referencing a different column in the CHECK.

## Medium — test coverage gaps

The PR #84 postmortem audit closed five of its six items via PRs
\#86–#90; this one item was scoped out of the kitchen-sink fixture
because the specific shapes need direct unit tests, not a DDL
that happens to round-trip.

- **`parseCreateTable` column-loop interactions.** `parseColumnDef`
  reads `cd.Type.Options.{Reference, KeyOpt, Invisible, …}`
  independently, but the loop in `parseCreateTable` calls
  `applyInlineColumnKey` and `buildFK` on the *same* column. Open
  combinations:
  - inline `UNIQUE × DEFAULT 'x'`
  - inline `REFERENCES × NOT NULL × ON UPDATE`
  - inline `PRIMARY KEY × CHECK (col > 0)` (column-level CHECK
    that vitess promotes to a table-level constraint — the
    promotion path interacts with the PK promotion path)
  Estimated 3–5 parser tests, a few hours.

## Low — tests / docs / release

- `CHANGELOG.md`.
- `.goreleaser.yml`.
- Renovate / dependabot config.
