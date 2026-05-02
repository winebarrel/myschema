package diff

import (
	"github.com/winebarrel/myschema/model"
	"github.com/winebarrel/myschema/parser"
	"github.com/winebarrel/orderedmap"
)

// ViewDiffResult holds the per-category statements emitted by DiffViews.
//
// CreateStmts run *after* table changes (a new view may reference a brand
// new table). DropStmts run *before* table changes (a view drop may free up
// columns the table needs to drop / alter).
type ViewDiffResult struct {
	CreateStmts         []string
	DropStmts           []string
	DisallowedDropStmts []string
}

// DiffViews compares two view maps and emits CREATE OR REPLACE / DROP VIEW
// statements. database is the default DB used to canonicalise SELECT bodies
// for comparison (so an unqualified `FROM users` in desired matches a
// catalog-side `FROM \`mydb\`.\`users\“).
func DiffViews(current, desired *orderedmap.Map[string, *model.View], database string, dc DropChecker) (*ViewDiffResult, error) {
	dc = NormalizeDropChecker(dc)
	res := &ViewDiffResult{}
	viewAllowed := dc.IsDropAllowed("view")

	// Creates / replaces.
	for name, dv := range desired.All() {
		cv, ok := current.GetOk(name)
		if ok {
			eq, err := viewDefEqual(cv.Definition, dv.Definition, database)
			if err != nil {
				return nil, err
			}
			if eq {
				continue
			}
		}
		res.CreateStmts = append(res.CreateStmts, dv.CreateSQL())
	}

	// Drops.
	for name, cv := range current.All() {
		if _, ok := desired.GetOk(name); ok {
			continue
		}
		drop := cv.DropSQL()
		if !viewAllowed {
			res.DisallowedDropStmts = append(res.DisallowedDropStmts, "-- skipped: "+drop)
			continue
		}
		res.DropStmts = append(res.DropStmts, drop)
	}

	return res, nil
}

// viewDefEqual normalises both definitions through pingcap parser+restore
// so that schema-qualification and formatting differences don't trigger
// spurious diffs. Falls back to byte equality when either side fails to
// parse (defensive — both sides are SELECT bodies and should always parse).
func viewDefEqual(a, b, database string) (bool, error) {
	if a == b {
		return true, nil
	}
	na, err := parser.NormalizeViewDefinition(a, database)
	if err != nil {
		return a == b, nil //nolint:nilerr
	}
	nb, err := parser.NormalizeViewDefinition(b, database)
	if err != nil {
		return a == b, nil //nolint:nilerr
	}
	return na == nb, nil
}
