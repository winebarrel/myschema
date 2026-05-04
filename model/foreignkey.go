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
//
// The owning table name is always emitted unqualified — myschema
// operates on one database per invocation. The REFERENCES target is
// emitted unqualified *only when it lives in the same database*; a
// cross-database FK (`REFERENCES other_db.parent (id)`) keeps its
// `other_db.` prefix so dropping it doesn't silently re-target the
// FK at a same-named table in the current database. (Cross-database
// FK *management* is intentionally out of scope — see CAVEATS.md
// "Foreign keys to tables in another database are passed through,
// not managed" — but emission must not silently reinterpret one the
// user wrote by hand.)
func (fk *ForeignKey) SQL() string {
	sql := "ALTER TABLE " + Ident(fk.Table) +
		" ADD CONSTRAINT " + Ident(fk.Name) +
		" FOREIGN KEY ("
	for i, c := range fk.Columns {
		if i > 0 {
			sql += ", "
		}
		sql += Ident(c)
	}
	ref := Ident(fk.RefTable)
	if fk.RefDB != "" && fk.RefDB != fk.Database {
		ref = Ident(fk.RefDB, fk.RefTable)
	}
	sql += ") REFERENCES " + ref + "("
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
