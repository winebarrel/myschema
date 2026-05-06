package diff

import (
	"fmt"
	"slices"

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
// for comparison: an unqualified `FROM users` in desired matches a
// catalog-side qualified form (FROM `mydb`.`users`) after both go
// through the canonicaliser.
//
// Order discipline:
//   - Creates / replaces are emitted in topological order of view-on-view
//     dependency: a view that selects from another view in the same set is
//     emitted *after* its dependencies.
//   - Drops are emitted in **reverse** topological order of the *current*
//     state, so a view that depends on another isn't left dangling.
func DiffViews(current, desired *orderedmap.Map[string, *model.View], database string, dc DropChecker) (*ViewDiffResult, error) {
	dc = NormalizeDropChecker(dc)
	res := &ViewDiffResult{}
	viewAllowed := dc.IsDropAllowed("view")

	desiredOrder, err := topoSortViews(desired, database)
	if err != nil {
		return nil, fmt.Errorf("topo-sort desired views: %w", err)
	}
	currentOrder, err := topoSortViews(current, database)
	if err != nil {
		return nil, fmt.Errorf("topo-sort current views: %w", err)
	}

	// Creates / replaces, in dependency order.
	for _, dv := range desiredOrder {
		cv, ok := current.GetOk(dv.FQVN())
		if ok {
			eq, err := viewEqual(cv, dv, database)
			if err != nil {
				return nil, err
			}
			if eq {
				continue
			}
		}
		res.CreateStmts = append(res.CreateStmts, dv.CreateSQL())
	}

	// Drops, in reverse dependency order so a parent view is gone before the
	// view it depended on.
	for i := len(currentOrder) - 1; i >= 0; i-- {
		cv := currentOrder[i]
		if _, ok := desired.GetOk(cv.FQVN()); ok {
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

// topoSortViews returns the views in `views` ordered so that each view comes
// after every other view in the same set that it references. Ties are broken
// by alphabetical FQVN so the output is deterministic. References to
// non-views (or to views outside `views`) are ignored. Returns an error on
// circular dependency.
func topoSortViews(views *orderedmap.Map[string, *model.View], database string) ([]*model.View, error) {
	// dep[k] = views that k depends on (subset of views' keys).
	// rev[k] = views that depend on k.
	// indeg[k] = unmet-dependency count.
	keys := make([]string, 0, views.Len())
	for k := range views.All() {
		keys = append(keys, k)
	}
	slices.Sort(keys)

	dep := make(map[string][]string, len(keys))
	rev := make(map[string][]string, len(keys))
	indeg := make(map[string]int, len(keys))

	for _, k := range keys {
		v := views.Get(k)
		refs, err := parser.ViewReferences(v.Definition, database)
		if err != nil {
			return nil, fmt.Errorf("view %s: %w", k, err)
		}
		for _, r := range refs {
			if r == k {
				continue
			}
			if _, ok := views.GetOk(r); !ok {
				continue
			}
			dep[k] = append(dep[k], r)
			rev[r] = append(rev[r], k)
			indeg[k]++
		}
	}

	// Kahn's algorithm with sorted insertion for deterministic ties.
	var queue []string
	for _, k := range keys {
		if indeg[k] == 0 {
			queue = append(queue, k)
		}
	}

	out := make([]*model.View, 0, len(keys))
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		out = append(out, views.Get(n))

		var next []string
		for _, m := range rev[n] {
			indeg[m]--
			if indeg[m] == 0 {
				next = append(next, m)
			}
		}
		slices.Sort(next)
		queue = append(queue, next...)
	}

	if len(out) != len(keys) {
		return nil, fmt.Errorf("circular view dependency detected among %d views", len(keys))
	}
	return out, nil
}

// viewEqual reports whether two views are operationally identical for
// diff purposes. The SELECT body goes through viewDefEqual (which
// normalises qualifiers, redundant aliases, and casing); the
// `WITH … CHECK OPTION` clause is compared after folding the empty
// string into "NONE" so the comparison stays in step with
// `(*View).CreateSQL()`, which treats both as "no WITH clause" and
// suppresses the keyword.
//
// Cols (the optional column-alias list `CREATE VIEW v (a, b) AS …`)
// is intentionally NOT compared here: catalog/views.go reads only
// `information_schema.VIEWS`, which exposes the SELECT body but not
// the user-supplied alias list (those live in
// `information_schema.COLUMNS` keyed by view+column). Until the
// catalog reader is taught to populate `model.View.Cols` from that
// source, a direct `slices.Equal` would treat every column-alias-list
// view as drifting on every plan. Cols changes therefore stay
// silently ignored as before, tracked separately as a TODO.
func viewEqual(a, b *model.View, database string) (bool, error) {
	if canonicalCheckOption(a.CheckOption) != canonicalCheckOption(b.CheckOption) {
		return false, nil
	}
	return viewDefEqual(a.Definition, b.Definition, database)
}

// canonicalCheckOption folds the empty string into "NONE" so a
// hand-built `model.View{}` (where `CheckOption` is the zero value)
// compares equal to the parser/catalog-populated `"NONE"`. Both
// shapes mean "no WITH … CHECK OPTION clause" — `(*View).CreateSQL()`
// already treats them identically.
func canonicalCheckOption(s string) string {
	if s == "" {
		return "NONE"
	}
	return s
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
