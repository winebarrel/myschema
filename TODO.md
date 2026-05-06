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

## Medium — test coverage gaps

The PR #84 postmortem audit closed five of its six items via PRs
\#86–#90; this one item was scoped out of the kitchen-sink fixture
because the specific shapes need direct unit tests, not a DDL
that happens to round-trip.

- **`parseCreateTable` column-loop interactions.** `parseColumnDef`
  reads `cd.Type.Options.{KeyOpt, Invisible, …}` independently,
  but the loop in `parseCreateTable` calls `applyInlineColumnKey`
  on the same column. Open combinations:
  - inline `UNIQUE × DEFAULT 'x'`
  - inline `PRIMARY KEY × CHECK (col > 0)` (column-level CHECK
    that vitess promotes to a table-level constraint — the
    promotion path interacts with the PK promotion path)
  Estimated 2–3 parser tests, a couple hours. (The original
  third combination, `inline REFERENCES × NOT NULL × ON UPDATE`,
  was eliminated by PR #92 — inline column-level `REFERENCES`
  now errors at parse time, so that interaction shape is no
  longer reachable.)

## Low — tests / docs / release

- `CHANGELOG.md`.
- `.goreleaser.yml`.
- Renovate / dependabot config.
