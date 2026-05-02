package diff

import (
	"fmt"
	"strings"

	"github.com/winebarrel/myschema/model"
	"github.com/winebarrel/orderedmap"
)

// applyTableRenames consumes RenameFrom directives on desired tables and
// emits ALTER TABLE … RENAME TO statements. After this pass the matching
// entry in `current` has been re-keyed to the new FQTN, so the rest of
// DiffTables sees the renamed table under its new name and proceeds with
// normal column / index / FK diffing — column data survives.
//
// Errors are surfaced to the caller (rather than silently falling back to
// DROP+CREATE) because a typo'd old name almost always means the user is
// about to lose data and would rather see the apply abort.
func applyTableRenames(current, desired *orderedmap.Map[string, *model.Table]) ([]string, error) {
	if dup, name := duplicateTableRenameSource(desired); dup {
		return nil, fmt.Errorf("renamed-from: source table %q is referenced by multiple tables — only one table may be renamed from a given source", name)
	}
	var stmts []string
	for newKey, dt := range desired.All() {
		if dt.RenameFrom == nil || *dt.RenameFrom == "" {
			continue
		}
		// Self-rename: directive lists the desired table's own name as
		// the source. Almost certainly a typo; treat as a no-op so we
		// don't generate `ALTER TABLE x RENAME TO x` (which MySQL
		// rejects on some versions and is a no-op on others).
		if *dt.RenameFrom == dt.Name {
			continue
		}
		oldKey := model.Ident(dt.Database, *dt.RenameFrom)
		ct, ok := current.GetOk(oldKey)
		if !ok {
			// Tolerate the case where the rename has already been
			// applied: if the new name is already present in current
			// and the old name isn't, we treat the directive as
			// satisfied (idempotent re-apply) — no statement, no error.
			if _, alreadyRenamed := current.GetOk(newKey); alreadyRenamed {
				continue
			}
			return nil, fmt.Errorf("renamed-from: source table %s not found in current schema", oldKey)
		}
		if _, dup := current.GetOk(newKey); dup && oldKey != newKey {
			return nil, fmt.Errorf("renamed-from: cannot rename %s to %s — destination already exists", oldKey, newKey)
		}
		stmts = append(stmts, "ALTER TABLE "+oldKey+" RENAME TO "+newKey+";")
		current.DeleteOk(oldKey)
		ct.Database = dt.Database
		ct.Name = dt.Name
		current.Set(newKey, ct)
		// Rewrite (RefDB, RefTable) on every other table's FKs that
		// pointed at the old name. Without this, a pure table rename
		// would diff each referencing FK as DROP FOREIGN KEY +
		// ADD CONSTRAINT (and fail under restrictive --allow-drop) even
		// though only the target name changed.
		rewriteFKRefTable(current, dt.Database, *dt.RenameFrom, dt.Database, dt.Name)
	}
	return stmts, nil
}

// rewriteFKRefTable walks every table in the map and updates any FK
// whose (RefDB, RefTable) matches (oldDB, oldName) to (newDB, newName).
// Used after a table rename so cross-table FK references stay quiet in
// the diff.
func rewriteFKRefTable(tables *orderedmap.Map[string, *model.Table], oldDB, oldName, newDB, newName string) {
	for _, t := range tables.CollectValues() {
		for _, fk := range t.ForeignKeys.CollectValues() {
			if fk.RefDB == oldDB && fk.RefTable == oldName {
				fk.RefDB = newDB
				fk.RefTable = newName
			}
		}
	}
}

// applyColumnRenames does the same trick at the column level. Returns the
// rename ALTER statements; the caller threads them in front of the normal
// column diff so any later MODIFY COLUMN sees the renamed column.
//
// Emits `ALTER TABLE … RENAME COLUMN`, which is MySQL 8.0+ syntax. This
// is consistent with the rest of myschema, which already requires 8.0
// for INVISIBLE indexes, CHECK constraints (8.0.16+), and other catalog
// features it reads (see AGENTS.md). Apply against an older server will
// fail at execution time with a syntax error, not silently — the user
// will see the offending RENAME COLUMN statement in the plan output.
func applyColumnRenames(fqtn string, current, desired *orderedmap.Map[string, *model.Column]) ([]string, error) {
	if dup, name := duplicateColumnRenameSource(desired); dup {
		return nil, fmt.Errorf("renamed-from: source column %s.%s is referenced by multiple columns", fqtn, name)
	}
	var stmts []string
	for newName, dc := range desired.All() {
		if dc.RenameFrom == nil || *dc.RenameFrom == "" {
			continue
		}
		oldName := *dc.RenameFrom
		if oldName == newName {
			continue // self-rename, see applyTableRenames for rationale
		}
		cc, ok := current.GetOk(oldName)
		if !ok {
			if _, alreadyRenamed := current.GetOk(newName); alreadyRenamed {
				continue
			}
			return nil, fmt.Errorf("renamed-from: source column %s.%s not found", fqtn, oldName)
		}
		if _, dup := current.GetOk(newName); dup && oldName != newName {
			return nil, fmt.Errorf("renamed-from: cannot rename %s.%s to %s — destination already exists", fqtn, oldName, newName)
		}
		stmts = append(stmts, "ALTER TABLE "+fqtn+" RENAME COLUMN "+model.Ident(oldName)+" TO "+model.Ident(newName)+";")
		current.DeleteOk(oldName)
		cc.Name = newName
		current.Set(newName, cc)
	}
	return stmts, nil
}

// rewriteIndexColumnRefs walks every index in current and rewrites any
// IndexPart.Column == old to new. Called after column renames complete so
// the catalog-side view stays consistent with the desired-side view of
// which columns the index covers — without this, indexEqual sees an
// (old col vs new col) mismatch and the index would be needlessly
// dropped + recreated.
//
// Functional / expression index parts (IndexPart.Expr) are NOT rewritten
// here: rewriting embedded column references inside an arbitrary SQL
// expression requires a real parser, and getting it subtly wrong is more
// dangerous than the alternative. An expression index that references a
// renamed column will surface as DROP+CREATE in the plan, which is
// functionally correct (the expression is unchanged metadata; the
// index entries are recomputed) and rare enough that the documented
// limitation suffices.
func rewriteIndexColumnRefs(indexes *orderedmap.Map[string, *model.Index], renames map[string]string) {
	if len(renames) == 0 {
		return
	}
	for _, idx := range indexes.CollectValues() {
		for i := range idx.Parts {
			if newName, ok := renames[idx.Parts[i].Column]; ok {
				idx.Parts[i].Column = newName
			}
		}
	}
}

// rewriteConstraintColumnRefs rewrites column references inside PRIMARY KEY
// constraints after a column rename. Without this, diffConstraints would
// see `(old_col)` in current vs `(new_col)` in desired and emit a
// destructive DROP PRIMARY KEY + ADD PRIMARY KEY — even though MySQL
// updates the PK metadata automatically alongside RENAME COLUMN.
//
// CHECK constraints carry a free-form expression in Definition; rewriting
// embedded column references inside arbitrary expressions is not safe
// without a full SQL expression parser. CHECK on a renamed column is
// rare and DROP+ADD on the CHECK is acceptable as the catalog and parser
// produce the same expression text either way.
func rewriteConstraintColumnRefs(constraints *orderedmap.Map[string, *model.Constraint], renames map[string]string) {
	if len(renames) == 0 {
		return
	}
	for _, con := range constraints.CollectValues() {
		if con.Type != model.PrimaryKeyConstraint {
			continue
		}
		changed := false
		for i, c := range con.Columns {
			if newName, ok := renames[c]; ok {
				con.Columns[i] = newName
				changed = true
			}
		}
		if changed {
			parts := make([]string, len(con.Columns))
			for i, c := range con.Columns {
				parts[i] = model.Ident(c)
			}
			con.Definition = "(" + strings.Join(parts, ", ") + ")"
		}
	}
}

// rewriteFKColumnRefs is the FK counterpart to rewriteIndexColumnRefs:
// walks every FK in current and rewrites Columns entries from old → new.
// Without this, fkEqual would diff `current.Columns=[old_col]` against
// `desired.Columns=[new_col]` and emit a destructive DROP FOREIGN KEY +
// ADD CONSTRAINT after every plain column rename — which would also fail
// under `--allow-drop=foreign_key` unset, since the FK drop is suppressed
// while the new ADD CONSTRAINT runs alone (a half-applied state).
//
// (db, name) identifies the table whose columns are being renamed.
// Self-referential FKs — those whose (RefDB, RefTable) points back at
// (db, name) — also have RefCols rewritten, which the cross-table pass
// in DiffTables explicitly skips because it short-circuits the renamed
// table itself.
func rewriteFKColumnRefs(fks *orderedmap.Map[string, *model.ForeignKey], db, name string, renames map[string]string) {
	if len(renames) == 0 {
		return
	}
	for _, fk := range fks.CollectValues() {
		for i, col := range fk.Columns {
			if newName, ok := renames[col]; ok {
				fk.Columns[i] = newName
			}
		}
		if fk.RefDB == db && fk.RefTable == name {
			for i, col := range fk.RefCols {
				if newName, ok := renames[col]; ok {
					fk.RefCols[i] = newName
				}
			}
		}
	}
}

// rewriteCrossTableFKRefCols handles the parent side of a column rename:
// for every table in `tables` other than (db, tableName), rewrites RefCols
// on any FK that points at this table when the referenced column name
// matches an entry in renames. Without this, renaming a referenced
// column would diff each referencing FK as DROP+ADD even though MySQL
// updates the FK's parent-side reference automatically when its parent
// column is renamed.
func rewriteCrossTableFKRefCols(tables *orderedmap.Map[string, *model.Table], db, tableName string, renames map[string]string) {
	if len(renames) == 0 {
		return
	}
	for _, t := range tables.CollectValues() {
		if t.Database == db && t.Name == tableName {
			continue // child-side rewrite handled by rewriteFKColumnRefs
		}
		for _, fk := range t.ForeignKeys.CollectValues() {
			if fk.RefDB != db || fk.RefTable != tableName {
				continue
			}
			for i, col := range fk.RefCols {
				if newName, ok := renames[col]; ok {
					fk.RefCols[i] = newName
				}
			}
		}
	}
}

// applyIndexRenames emits ALTER TABLE … RENAME INDEX statements. Same
// shape as the table/column version; the rename happens before the normal
// index diff so an index that was *only* renamed (no part / type change)
// produces no further DDL.
func applyIndexRenames(fqtn string, current, desired *orderedmap.Map[string, *model.Index]) ([]string, error) {
	if dup, name := duplicateIndexRenameSource(desired); dup {
		return nil, fmt.Errorf("renamed-from: source index %s.%s is referenced by multiple indexes", fqtn, name)
	}
	var stmts []string
	for newName, di := range desired.All() {
		if di.RenameFrom == nil || *di.RenameFrom == "" {
			continue
		}
		oldName := *di.RenameFrom
		if oldName == newName {
			continue // self-rename, see applyTableRenames for rationale
		}
		ci, ok := current.GetOk(oldName)
		if !ok {
			if _, alreadyRenamed := current.GetOk(newName); alreadyRenamed {
				continue
			}
			return nil, fmt.Errorf("renamed-from: source index %s.%s not found", fqtn, oldName)
		}
		if _, dup := current.GetOk(newName); dup && oldName != newName {
			return nil, fmt.Errorf("renamed-from: cannot rename index %s.%s to %s — destination already exists", fqtn, oldName, newName)
		}
		stmts = append(stmts, "ALTER TABLE "+fqtn+" RENAME INDEX "+model.Ident(oldName)+" TO "+model.Ident(newName)+";")
		current.DeleteOk(oldName)
		ci.Name = newName
		current.Set(newName, ci)
	}
	return stmts, nil
}

// duplicateTableRenameSource scans desired tables and reports if two
// distinct entries declare the same RenameFrom value (so they'd both
// try to consume the same current-side row). Returns (true, oldName)
// on the first conflict found, otherwise (false, "").
func duplicateTableRenameSource(desired *orderedmap.Map[string, *model.Table]) (bool, string) {
	seen := map[string]bool{}
	for _, dt := range desired.CollectValues() {
		if dt.RenameFrom == nil || *dt.RenameFrom == "" {
			continue
		}
		key := dt.Database + "." + *dt.RenameFrom
		if seen[key] {
			return true, *dt.RenameFrom
		}
		seen[key] = true
	}
	return false, ""
}

// duplicateColumnRenameSource is the column counterpart of
// duplicateTableRenameSource — scoped to one table's column map.
func duplicateColumnRenameSource(desired *orderedmap.Map[string, *model.Column]) (bool, string) {
	seen := map[string]bool{}
	for _, dc := range desired.CollectValues() {
		if dc.RenameFrom == nil || *dc.RenameFrom == "" {
			continue
		}
		if seen[*dc.RenameFrom] {
			return true, *dc.RenameFrom
		}
		seen[*dc.RenameFrom] = true
	}
	return false, ""
}

// duplicateIndexRenameSource is the index counterpart.
func duplicateIndexRenameSource(desired *orderedmap.Map[string, *model.Index]) (bool, string) {
	seen := map[string]bool{}
	for _, di := range desired.CollectValues() {
		if di.RenameFrom == nil || *di.RenameFrom == "" {
			continue
		}
		if seen[*di.RenameFrom] {
			return true, *di.RenameFrom
		}
		seen[*di.RenameFrom] = true
	}
	return false, ""
}

// columnRenameMap collects desired-side column renames as old→new for use
// by rewriteIndexColumnRefs after the rename ALTERs have been emitted.
func columnRenameMap(desired *orderedmap.Map[string, *model.Column]) map[string]string {
	var m map[string]string
	for newName, dc := range desired.All() {
		if dc.RenameFrom == nil || *dc.RenameFrom == "" {
			continue
		}
		if m == nil {
			m = map[string]string{}
		}
		m[*dc.RenameFrom] = newName
	}
	return m
}
