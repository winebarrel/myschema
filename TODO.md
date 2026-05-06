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

## Low — tests / docs / release

- `CHANGELOG.md`.
- `.goreleaser.yml`.
- Renovate / dependabot config.
