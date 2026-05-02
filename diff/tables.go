package diff

import (
	"strings"

	"github.com/winebarrel/myschema/model"
	"github.com/winebarrel/orderedmap"
)

// TableDiffResult separates FK operations from other statements so callers
// can order them: FK drops first, then table/column/constraint/index changes,
// then FK adds last.
type TableDiffResult struct {
	FKDropStmts         []string
	Stmts               []string
	FKAddStmts          []string
	DropStmts           []string
	DisallowedDropStmts []string
}

// DiffTables produces the DDL needed to bring the current schema into the
// shape described by desired.
func DiffTables(current, desired *orderedmap.Map[string, *model.Table], dc DropChecker) (*TableDiffResult, error) {
	dc = NormalizeDropChecker(dc)
	res := &TableDiffResult{}

	// New tables: emit CREATE TABLE, then per-table secondary indexes and FKs.
	for k, dt := range desired.All() {
		if _, ok := current.GetOk(k); ok {
			continue
		}
		res.Stmts = append(res.Stmts, dt.SQL())
		for _, idx := range dt.Indexes.CollectValues() {
			if idx.Primary {
				continue
			}
			res.Stmts = append(res.Stmts, idx.SQL())
		}
		for _, fk := range dt.ForeignKeys.CollectValues() {
			res.FKAddStmts = append(res.FKAddStmts, fk.SQL())
		}
	}

	// Modified tables.
	for k, dt := range desired.All() {
		ct, ok := current.GetOk(k)
		if !ok {
			continue
		}
		sub := diffTable(ct, dt, dc)
		res.FKDropStmts = append(res.FKDropStmts, sub.FKDropStmts...)
		res.Stmts = append(res.Stmts, sub.Stmts...)
		res.FKAddStmts = append(res.FKAddStmts, sub.FKAddStmts...)
		res.DisallowedDropStmts = append(res.DisallowedDropStmts, sub.DisallowedDropStmts...)
	}

	// Dropped tables: drop FKs first to avoid dependency errors.
	tableAllowed := dc.IsDropAllowed("table")
	for k, ct := range current.All() {
		if _, ok := desired.GetOk(k); ok {
			continue
		}
		if !tableAllowed {
			for name := range ct.ForeignKeys.Keys() {
				res.DisallowedDropStmts = append(res.DisallowedDropStmts,
					"-- skipped: ALTER TABLE "+k+" DROP FOREIGN KEY "+model.Ident(name)+";")
			}
			res.DisallowedDropStmts = append(res.DisallowedDropStmts, "-- skipped: DROP TABLE "+k+";")
			continue
		}
		for name := range ct.ForeignKeys.Keys() {
			res.FKDropStmts = append(res.FKDropStmts,
				"ALTER TABLE "+k+" DROP FOREIGN KEY "+model.Ident(name)+";")
		}
		res.DropStmts = append(res.DropStmts, "DROP TABLE "+k+";")
	}

	return res, nil
}

type tableDiffResult struct {
	FKDropStmts         []string
	Stmts               []string
	FKAddStmts          []string
	DisallowedDropStmts []string
}

func diffTable(current, desired *model.Table, dc DropChecker) *tableDiffResult {
	res := &tableDiffResult{}
	fqtn := desired.FQTN()

	colStmts, colDisallowed := diffColumns(fqtn, current.Columns, desired.Columns, dc)
	res.Stmts = append(res.Stmts, colStmts...)
	res.DisallowedDropStmts = append(res.DisallowedDropStmts, colDisallowed...)

	conStmts, conDisallowed := diffConstraints(fqtn, current.Constraints, desired.Constraints, dc)
	res.Stmts = append(res.Stmts, conStmts...)
	res.DisallowedDropStmts = append(res.DisallowedDropStmts, conDisallowed...)

	idxStmts, idxDisallowed := diffIndexes(fqtn, current.Indexes, desired.Indexes, dc)
	res.Stmts = append(res.Stmts, idxStmts...)
	res.DisallowedDropStmts = append(res.DisallowedDropStmts, idxDisallowed...)

	fkDrops, fkAdds, fkDisallowed := diffForeignKeys(fqtn, current.ForeignKeys, desired.ForeignKeys, dc)
	res.FKDropStmts = append(res.FKDropStmts, fkDrops...)
	res.FKAddStmts = append(res.FKAddStmts, fkAdds...)
	res.DisallowedDropStmts = append(res.DisallowedDropStmts, fkDisallowed...)

	return res
}

func diffColumns(fqtn string, current, desired *orderedmap.Map[string, *model.Column], dc DropChecker) (stmts, disallowed []string) {
	colAllowed := dc.IsDropAllowed("column")

	// New / changed columns
	for name, dc2 := range desired.All() {
		cc, ok := current.GetOk(name)
		if !ok {
			stmts = append(stmts, addColumnSQL(fqtn, dc2))
			continue
		}
		if !columnEqual(cc, dc2) {
			stmts = append(stmts, modifyColumnSQL(fqtn, dc2))
		}
	}
	// Dropped columns
	for name := range current.Keys() {
		if _, ok := desired.GetOk(name); ok {
			continue
		}
		drop := "ALTER TABLE " + fqtn + " DROP COLUMN " + model.Ident(name) + ";"
		if !colAllowed {
			disallowed = append(disallowed, "-- skipped: "+drop)
			continue
		}
		stmts = append(stmts, drop)
	}
	return
}

func columnEqual(a, b *model.Column) bool {
	if a.TypeName != b.TypeName {
		return false
	}
	if a.NotNull != b.NotNull {
		return false
	}
	if !ptrEq(a.Default, b.Default) {
		return false
	}
	if !ptrEq(a.OnUpdate, b.OnUpdate) {
		return false
	}
	if a.AutoIncrement != b.AutoIncrement {
		return false
	}
	if !ptrEq(a.Generated, b.Generated) {
		return false
	}
	if a.Stored != b.Stored {
		return false
	}
	if !ptrEq(a.Comment, b.Comment) {
		return false
	}
	return true
}

func ptrEq[T comparable](a, b *T) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

func addColumnSQL(fqtn string, c *model.Column) string {
	return "ALTER TABLE " + fqtn + " ADD COLUMN " + columnDefSQL(c) + ";"
}

func modifyColumnSQL(fqtn string, c *model.Column) string {
	return "ALTER TABLE " + fqtn + " MODIFY COLUMN " + columnDefSQL(c) + ";"
}

func columnDefSQL(c *model.Column) string {
	var b strings.Builder
	b.WriteString(model.Ident(c.Name))
	b.WriteString(" ")
	b.WriteString(c.TypeName)
	if c.NotNull {
		b.WriteString(" NOT NULL")
	}
	if c.Default != nil {
		b.WriteString(" DEFAULT ")
		b.WriteString(*c.Default)
	}
	if c.OnUpdate != nil {
		b.WriteString(" ON UPDATE ")
		b.WriteString(*c.OnUpdate)
	}
	if c.AutoIncrement {
		b.WriteString(" AUTO_INCREMENT")
	}
	if c.Comment != nil {
		b.WriteString(" COMMENT ")
		b.WriteString(model.QuoteLiteral(*c.Comment))
	}
	return b.String()
}

func diffConstraints(fqtn string, current, desired *orderedmap.Map[string, *model.Constraint], dc DropChecker) (stmts, disallowed []string) {
	conAllowed := dc.IsDropAllowed("constraint")

	for name, ccon := range current.All() {
		dcon, ok := desired.GetOk(name)
		if ok && constraintEqual(ccon, dcon) {
			continue
		}
		// PK uses DROP PRIMARY KEY, not DROP CONSTRAINT.
		drop := dropConstraintSQL(fqtn, ccon)
		if !ok && !conAllowed {
			disallowed = append(disallowed, "-- skipped: "+drop)
			continue
		}
		stmts = append(stmts, drop)
	}
	for name, dcon := range desired.All() {
		ccon, ok := current.GetOk(name)
		if ok && constraintEqual(ccon, dcon) {
			continue
		}
		stmts = append(stmts, addConstraintSQL(fqtn, dcon))
	}
	_ = stmts
	return
}

func dropConstraintSQL(fqtn string, c *model.Constraint) string {
	switch c.Type {
	case model.PrimaryKeyConstraint:
		return "ALTER TABLE " + fqtn + " DROP PRIMARY KEY;"
	default:
		return "ALTER TABLE " + fqtn + " DROP CHECK " + model.Ident(c.Name) + ";"
	}
}

func addConstraintSQL(fqtn string, c *model.Constraint) string {
	switch c.Type {
	case model.PrimaryKeyConstraint:
		return "ALTER TABLE " + fqtn + " ADD PRIMARY KEY " + c.Definition + ";"
	case model.CheckConstraint:
		return "ALTER TABLE " + fqtn + " ADD CONSTRAINT " + model.Ident(c.Name) + " " + c.Definition + ";"
	}
	return "ALTER TABLE " + fqtn + " ADD CONSTRAINT " + model.Ident(c.Name) + " " + c.Definition + ";"
}

func constraintEqual(a, b *model.Constraint) bool {
	if a.Type != b.Type {
		return false
	}
	if normalizeDef(a.Definition) != normalizeDef(b.Definition) {
		return false
	}
	if a.Type == model.CheckConstraint && a.Enforced != b.Enforced {
		return false
	}
	return true
}

func normalizeDef(s string) string {
	return strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(s, " ", ""), "`", ""))
}

func diffIndexes(fqtn string, current, desired *orderedmap.Map[string, *model.Index], dc DropChecker) (stmts, disallowed []string) {
	idxAllowed := dc.IsDropAllowed("index")

	for name, ci := range current.All() {
		if ci.Primary {
			continue // PRIMARY KEY is handled via Constraint diff
		}
		di, ok := desired.GetOk(name)
		if ok && indexEqual(ci, di) {
			continue
		}
		drop := "ALTER TABLE " + fqtn + " DROP INDEX " + model.Ident(name) + ";"
		if !ok && !idxAllowed {
			disallowed = append(disallowed, "-- skipped: "+drop)
			continue
		}
		stmts = append(stmts, drop)
	}
	for name, di := range desired.All() {
		if di.Primary {
			continue
		}
		ci, ok := current.GetOk(name)
		if ok && indexEqual(ci, di) {
			continue
		}
		stmts = append(stmts, di.SQL())
	}
	return
}

func indexEqual(a, b *model.Index) bool {
	if a.KeyType != b.KeyType {
		return false
	}
	if len(a.Parts) != len(b.Parts) {
		return false
	}
	for i := range a.Parts {
		if a.Parts[i] != b.Parts[i] {
			return false
		}
	}
	if normalizeIndexType(a.IndexType) != normalizeIndexType(b.IndexType) {
		return false
	}
	if a.Invisible != b.Invisible {
		return false
	}
	return true
}

// normalizeIndexType folds the InnoDB default ("BTREE") into "" so that an
// implicit `CREATE INDEX … (col)` (no USING clause) compares equal to the
// catalog's "BTREE". Other index types (HASH, FULLTEXT, SPATIAL) are returned
// uppercased and unchanged.
func normalizeIndexType(s string) string {
	up := strings.ToUpper(s)
	if up == "BTREE" {
		return ""
	}
	return up
}

func diffForeignKeys(fqtn string, current, desired *orderedmap.Map[string, *model.ForeignKey], dc DropChecker) (drops, adds, disallowed []string) {
	fkAllowed := dc.IsDropAllowed("foreign_key")

	for name, cf := range current.All() {
		df, ok := desired.GetOk(name)
		if ok && fkEqual(cf, df) {
			continue
		}
		drop := "ALTER TABLE " + fqtn + " DROP FOREIGN KEY " + model.Ident(name) + ";"
		if !ok && !fkAllowed {
			disallowed = append(disallowed, "-- skipped: "+drop)
			continue
		}
		drops = append(drops, drop)
	}
	for name, df := range desired.All() {
		cf, ok := current.GetOk(name)
		if ok && fkEqual(cf, df) {
			continue
		}
		adds = append(adds, df.SQL())
	}
	return
}

func fkEqual(a, b *model.ForeignKey) bool {
	if !sliceEq(a.Columns, b.Columns) || !sliceEq(a.RefCols, b.RefCols) {
		return false
	}
	if a.RefDB != b.RefDB || a.RefTable != b.RefTable {
		return false
	}
	if a.OnDelete != b.OnDelete || a.OnUpdate != b.OnUpdate {
		return false
	}
	if a.MatchType != b.MatchType {
		return false
	}
	return true
}

func sliceEq[T comparable](a, b []T) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
