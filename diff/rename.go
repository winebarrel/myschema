package diff

import (
	"fmt"

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
	var stmts []string
	for newKey, dt := range desired.All() {
		if dt.RenameFrom == nil || *dt.RenameFrom == "" {
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
	}
	return stmts, nil
}

// applyColumnRenames does the same trick at the column level. Returns the
// rename ALTER statements; the caller threads them in front of the normal
// column diff so any later MODIFY COLUMN sees the renamed column.
func applyColumnRenames(fqtn string, current, desired *orderedmap.Map[string, *model.Column]) ([]string, error) {
	var stmts []string
	for newName, dc := range desired.All() {
		if dc.RenameFrom == nil || *dc.RenameFrom == "" {
			continue
		}
		oldName := *dc.RenameFrom
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
		// Propagate the rename to any current-side index that references
		// the old column name, so the index diff doesn't subsequently see
		// "current uses old_col, desired uses new_col" and emit a needless
		// DROP+CREATE.
	}
	return stmts, nil
}

// rewriteIndexColumnRefs walks every index in current and rewrites any
// IndexPart.Column == old to new. Called after column renames complete so
// the catalog-side view stays consistent with the desired-side view of
// which columns the index covers.
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

// applyIndexRenames emits ALTER TABLE … RENAME INDEX statements. Same
// shape as the table/column version; the rename happens before the normal
// index diff so an index that was *only* renamed (no part / type change)
// produces no further DDL.
func applyIndexRenames(fqtn string, current, desired *orderedmap.Map[string, *model.Index]) ([]string, error) {
	var stmts []string
	for newName, di := range desired.All() {
		if di.RenameFrom == nil || *di.RenameFrom == "" {
			continue
		}
		oldName := *di.RenameFrom
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
