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
//
// CAVEAT: rename handling mutates `current` in place (re-keys entries
// after table renames; updates Name/Database, FK Columns, FK RefCols,
// index parts, and PK constraint columns to match desired's view of
// the world). Callers that want to reuse `current` after calling
// DiffTables should clone it first. The whole-program flow in
// diff_all.go builds a fresh `current` per invocation, so this is fine
// for production use; the constraint matters mainly for tests that
// share fixtures across subtests.
func DiffTables(current, desired *orderedmap.Map[string, *model.Table], dc DropChecker) (*TableDiffResult, error) {
	dc = NormalizeDropChecker(dc)
	res := &TableDiffResult{}

	// Rename pass first: ALTER TABLE … RENAME TO is applied before any
	// other table-level diffing, and the matching current entry is
	// re-keyed so the rest of the pipeline sees the renamed table under
	// its new FQTN. New / modified / dropped detection below then works
	// without false negatives.
	renameStmts, err := applyTableRenames(current, desired)
	if err != nil {
		return nil, err
	}
	res.Stmts = append(res.Stmts, renameStmts...)

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

	// Cross-table column-rename pass: when a column on a parent table
	// is renamed, every other table's FK that references it needs its
	// RefCols rewritten so the FK doesn't subsequently diff as DROP+ADD.
	// Same-table FK Columns and PK / index column refs are handled
	// inside diffTable itself.
	for k, dt := range desired.All() {
		if _, ok := current.GetOk(k); !ok {
			continue
		}
		renames := columnRenameMap(dt.Columns)
		if len(renames) == 0 {
			continue
		}
		rewriteCrossTableFKRefCols(current, dt.Database, dt.Name, renames)
	}

	// Modified tables.
	for k, dt := range desired.All() {
		ct, ok := current.GetOk(k)
		if !ok {
			continue
		}
		sub, err := diffTable(ct, dt, dc)
		if err != nil {
			return nil, err
		}
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

func diffTable(current, desired *model.Table, dc DropChecker) (*tableDiffResult, error) {
	res := &tableDiffResult{}
	fqtn := desired.FQTN()

	// Column rename pass first, so the index-rename pass below and the
	// regular column / index / FK diffs see the renamed objects under
	// their new names. Index parts AND FK column lists that referenced
	// the old column names are rewritten in place (current side only)
	// so indexEqual / fkEqual stay quiet for objects that should NOT
	// trigger a DROP+CREATE / DROP+ADD.
	renames := columnRenameMap(desired.Columns)
	colRenameStmts, err := applyColumnRenames(fqtn, current.Columns, desired.Columns)
	if err != nil {
		return nil, err
	}
	res.Stmts = append(res.Stmts, colRenameStmts...)
	rewriteIndexColumnRefs(current.Indexes, renames)
	rewriteFKColumnRefs(current.ForeignKeys, current.Database, current.Name, renames)
	rewriteConstraintColumnRefs(current.Constraints, current.Indexes, renames)

	idxRenameStmts, err := applyIndexRenames(fqtn, current.Indexes, desired.Indexes)
	if err != nil {
		return nil, err
	}
	res.Stmts = append(res.Stmts, idxRenameStmts...)

	colStmts, colDisallowed := diffColumns(fqtn, current.Columns, desired.Columns, dc)
	res.Stmts = append(res.Stmts, colStmts...)
	res.DisallowedDropStmts = append(res.DisallowedDropStmts, colDisallowed...)

	conStmts, conDisallowed := diffConstraints(fqtn, current.Constraints, desired.Constraints, dc)
	res.Stmts = append(res.Stmts, conStmts...)
	res.DisallowedDropStmts = append(res.DisallowedDropStmts, conDisallowed...)

	// Compute the set of columns that will *actually* be dropped (not
	// merely desired-to-be-dropped). diffIndexes uses this to suppress an
	// explicit `DROP INDEX` on indexes whose every part is a dropped
	// column — MySQL drops those indexes automatically alongside the
	// column, so a redundant `DROP INDEX` would error 1091. If
	// --allow-drop=column is unset the column won't actually go away,
	// so we must NOT suppress the index drop (the index would otherwise
	// be left orphaned-yet-unmanaged on a column that's still there).
	var dropped map[string]bool
	if dc.IsDropAllowed("column") {
		names := droppedColumns(current.Columns, desired.Columns)
		dropped = make(map[string]bool, len(names))
		for _, n := range names {
			dropped[n] = true
		}
	}
	idxStmts, idxDisallowed := diffIndexes(fqtn, current.Indexes, desired.Indexes, dc, dropped)
	res.Stmts = append(res.Stmts, idxStmts...)
	res.DisallowedDropStmts = append(res.DisallowedDropStmts, idxDisallowed...)

	fkDrops, fkAdds, fkDisallowed := diffForeignKeys(fqtn, current.ForeignKeys, desired.ForeignKeys, dc)
	res.FKDropStmts = append(res.FKDropStmts, fkDrops...)
	res.FKAddStmts = append(res.FKAddStmts, fkAdds...)
	res.DisallowedDropStmts = append(res.DisallowedDropStmts, fkDisallowed...)

	return res, nil
}

// droppedColumns returns the names of columns present in current but
// absent from desired — i.e. columns the diff is about to remove from
// the table — in current's iteration order.
func droppedColumns(current, desired *orderedmap.Map[string, *model.Column]) []string {
	var out []string
	for name := range current.Keys() {
		if _, ok := desired.GetOk(name); !ok {
			out = append(out, name)
		}
	}
	return out
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
	for _, name := range droppedColumns(current, desired) {
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
	if !equalExprPtr(a.Default, b.Default) {
		return false
	}
	if !equalExprPtr(a.OnUpdate, b.OnUpdate) {
		return false
	}
	if a.AutoIncrement != b.AutoIncrement {
		return false
	}
	if !equalExprPtr(a.Generated, b.Generated) {
		return false
	}
	if a.Stored != b.Stored {
		return false
	}
	if !ptrEq(a.Comment, b.Comment) {
		return false
	}
	// CharacterSet / Collation are intentionally NOT compared here: the
	// table's default charset/collation propagates to every column at the
	// catalog level, so a column with no explicit `CHARACTER SET` in the
	// user's SQL would always look "different" from the catalog's
	// table-default-inherited value. Surfacing column-level charset diffs
	// requires resolving that inheritance — see TODO.md.
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

// addColumnSQL / modifyColumnSQL share model.ColumnDefSQL with CREATE TABLE
// so that ALTER also carries CHARACTER SET / COLLATE / GENERATED clauses.
func addColumnSQL(fqtn string, c *model.Column) string {
	return "ALTER TABLE " + fqtn + " ADD COLUMN " + model.ColumnDefSQL(c) + ";"
}

func modifyColumnSQL(fqtn string, c *model.Column) string {
	return "ALTER TABLE " + fqtn + " MODIFY COLUMN " + model.ColumnDefSQL(c) + ";"
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
	if a.Type == model.CheckConstraint {
		if !equalCheckDef(a.Definition, b.Definition) {
			return false
		}
		if a.Enforced != b.Enforced {
			return false
		}
		return true
	}
	// PRIMARY KEY / UNIQUE definitions are simple `(col1, col2)` lists; the
	// loose normaliser below tolerates the casing / spacing / backtick
	// jitter we see between catalog and parser sides.
	return looseEqual(a.Definition, b.Definition)
}

// looseEqual is the textual fallback comparison used for non-expression
// constraint definitions (PRIMARY KEY / UNIQUE column lists). It folds
// case, drops whitespace, and strips backticks — enough for the
// `(col1, col2)` shape the catalog and parser both produce, without
// needing a full SQL parse.
func looseEqual(a, b string) bool {
	norm := func(s string) string {
		return strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(s, " ", ""), "`", ""))
	}
	return norm(a) == norm(b)
}

func diffIndexes(fqtn string, current, desired *orderedmap.Map[string, *model.Index], dc DropChecker, droppedCols map[string]bool) (stmts, disallowed []string) {
	idxAllowed := dc.IsDropAllowed("index")

	for name, ci := range current.All() {
		if ci.Primary {
			continue // PRIMARY KEY is handled via Constraint diff
		}
		di, ok := desired.GetOk(name)
		if ok && indexEqual(ci, di) {
			continue
		}
		// MySQL automatically removes any index whose every column is
		// dropped in the same ALTER TABLE flow. An explicit DROP INDEX
		// after that errors 1091. This holds for both pure removals and
		// for the DROP+CREATE replacement path (where the desired index
		// keeps the same name but on different columns) — skip the DROP
		// in either case; the desired-side loop below still emits the
		// fresh CREATE INDEX for the replacement variant.
		if allPartsDropped(ci, droppedCols) {
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

// allPartsDropped reports whether every column-typed part of idx is
// being dropped from the same table. Indexes with expression parts (no
// column name) don't count — we don't try to track expression-column
// dependencies here.
func allPartsDropped(idx *model.Index, droppedCols map[string]bool) bool {
	if len(idx.Parts) == 0 {
		return false
	}
	for _, p := range idx.Parts {
		if p.Column == "" || !droppedCols[p.Column] {
			return false
		}
	}
	return true
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
