package model

// ForeignKey models a MySQL foreign key constraint.
type ForeignKey struct {
	Name      string
	Database  string
	Table     string
	Columns   []string
	RefDB     string
	RefTable  string
	RefCols   []string
	OnDelete  string // empty when not specified; uppercased: RESTRICT, CASCADE, SET NULL, NO ACTION, SET DEFAULT
	OnUpdate  string
	MatchType string // empty, FULL, PARTIAL, SIMPLE
	// RenameFrom: previous FK name from a `-- myschema:renamed-from`
	// directive on the desired side. MySQL has no in-place RENAME
	// FOREIGN KEY, so this is consumed only as a typo guard at plan
	// time; the diff still emits DROP+ADD.
	RenameFrom *string
}

// SQL builds the standalone ALTER TABLE ... ADD CONSTRAINT statement.
func (fk *ForeignKey) SQL() string {
	sql := "ALTER TABLE " + Ident(fk.Database, fk.Table) +
		" ADD CONSTRAINT " + Ident(fk.Name) +
		" FOREIGN KEY ("
	for i, c := range fk.Columns {
		if i > 0 {
			sql += ", "
		}
		sql += Ident(c)
	}
	sql += ") REFERENCES " + Ident(fk.RefDB, fk.RefTable) + "("
	for i, c := range fk.RefCols {
		if i > 0 {
			sql += ", "
		}
		sql += Ident(c)
	}
	sql += ")"
	if fk.MatchType != "" {
		sql += " MATCH " + fk.MatchType
	}
	if fk.OnDelete != "" {
		sql += " ON DELETE " + fk.OnDelete
	}
	if fk.OnUpdate != "" {
		sql += " ON UPDATE " + fk.OnUpdate
	}
	return sql + ";"
}
