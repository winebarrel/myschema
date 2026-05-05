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

## Medium — test coverage audit

The PR #84 review caught a real bug (catalog `loadColumns` leaked the
trailing `INVISIBLE` token into `Column.OnUpdate` when both attributes
sat on the same row) that the original test suite missed. Pattern:
isolated single-attribute tests pass while combinations of attributes
sharing the same parser state break. Below is a prioritised audit of
where the same bug class could be hiding; each item lists the surface,
the missing combination, and the realistic harm.

- **Composed-state EXTRA / option parsing in `catalog/loadColumns`
  and `loadIndexes`.** Both functions read multiple tokens from a
  single shared string (`extraUp` for columns, the per-row index
  metadata for indexes). Existing tests cover each token in
  isolation; combinations don't run on the same row. Add matrix-
  shape tests:
  - Columns: `AUTO_INCREMENT × INVISIBLE`, `GENERATED (STORED) ×
    INVISIBLE`, `DEFAULT_GENERATED + ON UPDATE + INVISIBLE`,
    `GENERATED + COMMENT`. The PR #84 fix already covers
    `ON UPDATE × INVISIBLE`; the rest are open.
  - Indexes: `UNIQUE × INVISIBLE × COMMENT`, `PRIMARY × USING BTREE`,
    `FULLTEXT × COMMENT`, `SPATIAL × prefix-length`, `INVISIBLE`
    on a multi-column key.
  Estimated 8–12 catalog tests, half-day. Highest payoff per hour
  because it directly targets the bug class that hit on PR #84.

- **`parseCreateTable` column-loop interactions.** `parseColumnDef`
  reads `cd.Type.Options.{Reference, KeyOpt, Invisible, …}`
  independently, but the loop in `parseCreateTable` calls
  `applyInlineColumnKey` and `buildFK` on the *same* column. Open
  combinations:
  - inline `UNIQUE × DEFAULT 'x'`
  - inline `REFERENCES × NOT NULL × ON UPDATE`
  - inline `PRIMARY KEY × CHECK (col > 0)` (column-level CHECK that
    vitess promotes to a table-level constraint — the promotion
    path interacts with the PK promotion path)
  Estimated 3–5 parser tests, a few hours.

- **Emitter ordering tests (`columnDefSQL`, `indexInlineSQL`,
  `constraintInlineSQL`).** PR #84 added an index-comparison
  ordering pin for `INVISIBLE`'s position. The other emitters
  still rely on `Contains` (order-blind). Add index-comparison
  pins for:
  - `columnDefSQL` with all attributes set: `CHARSET → COLLATE →
    GENERATED → NOT NULL → DEFAULT → ON UPDATE → AUTO_INCREMENT →
    INVISIBLE → COMMENT`
  - `indexInlineSQL` / `Index.SQL()`: `UNIQUE → KEY_BLOCK_SIZE →
    USING → INVISIBLE → COMMENT`
  - `constraintInlineSQL`: PK / CHECK with optional `NOT ENFORCED`
    suffix
  Estimated 3 tests, 1–2 hours.

- **Diff cross-cutting / order-sensitivity.** Mutators run in
  sequence on the same `current` map (table renames → column
  renames → cross-table FK ref rewrites → diffTable). Existing
  `rename_test.go` covers the 1-to-1 cases. Open shapes:
  - column rename + drop column with the same final name (name
    collision)
  - index rename + DROP INDEX in same plan
  - partition diff + DEFAULT CHARSET change with `--bulk-alter`
  Estimated 3–4 diff tests, half-day.

- **Directive composition.** Statement-level mutual-exclusion is
  pinned (execute / renamed-from / convert-charset). Inline
  directives across multiple kinds in one CREATE TABLE aren't
  pinned: column rename + index rename + constraint rename in the
  same body should keep their classification per-kind so a
  KEY-named-after-its-column doesn't compete for the column
  rename's directive. Add 2–3 parser tests.

- **"Kitchen-sink" round-trip fixture.** One YAML fixture that
  exercises every supported attribute on one table (column
  attribute matrix, multiple index types, FK, RANGE PARTITION,
  CHECK, comments) and round-trips clean (`verify_no_drift:
  true`). One fixture, half-day, but the regression net is wide:
  any future attribute interaction bug surfaces here before
  shipping. Highest single-test ROI.

Combined estimated effort: 2.5–3 days for the whole audit.
Recommended order: composed-state catalog tests → kitchen-sink
fixture (catches anything the matrix tests miss) → emitter ordering
→ diff cross-cutting → directive composition. Found during the
PR #84 review postmortem.

## Low — tests / docs / release

- `CHANGELOG.md`.
- `.goreleaser.yml`.
- Renovate / dependabot config.
