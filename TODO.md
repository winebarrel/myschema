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

