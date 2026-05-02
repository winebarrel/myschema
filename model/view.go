package model

import (
	"strings"

	"github.com/winebarrel/orderedmap"
)

// View models a MySQL view. Definition is the SELECT body (no leading
// "AS"). Cols is the optional column-alias list from `CREATE VIEW name (a, b) AS …`.
//
// Algorithm / Definer / Security / CheckOption are catalogued but the v1 diff
// does not act on changes to these fields — see TODO.md.
type View struct {
	Database    string
	Name        string
	Definition  string
	Cols        []string
	Algorithm   string
	Definer     string
	Security    string
	CheckOption string // empty / "CASCADED" / "LOCAL" / "NONE"
}

// FQVN returns the database-qualified view name (database.view).
func (v *View) FQVN() string {
	return Ident(v.Database, v.Name)
}

// CreateSQL renders a CREATE OR REPLACE VIEW statement. We always use OR
// REPLACE so the diff doesn't have to track create-vs-replace separately —
// MySQL applies the new definition atomically either way.
func (v *View) CreateSQL() string {
	var b strings.Builder
	b.WriteString("CREATE OR REPLACE VIEW ")
	b.WriteString(v.FQVN())
	if len(v.Cols) > 0 {
		b.WriteString(" (")
		for i, c := range v.Cols {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(Ident(c))
		}
		b.WriteString(")")
	}
	b.WriteString(" AS ")
	b.WriteString(v.Definition)
	if v.CheckOption != "" && v.CheckOption != "NONE" {
		b.WriteString(" WITH ")
		b.WriteString(v.CheckOption)
		b.WriteString(" CHECK OPTION")
	}
	b.WriteString(";")
	return b.String()
}

// DropSQL renders the corresponding DROP VIEW statement.
func (v *View) DropSQL() string {
	return "DROP VIEW " + v.FQVN() + ";"
}

// ViewToSQL is the dump representation of a single view.
func ViewToSQL(v *View) string {
	return "-- " + v.FQVN() + "\n" + v.CreateSQL()
}

// ViewsToSQL renders all views in order, separated by blank lines.
func ViewsToSQL(views *orderedmap.Map[string, *View]) string {
	parts := make([]string, 0, views.Len())
	for _, v := range views.CollectValues() {
		parts = append(parts, ViewToSQL(v))
	}
	return strings.Join(parts, "\n\n")
}
