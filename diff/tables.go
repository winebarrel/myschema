package diff

import (
	"fmt"
	"slices"
	"strings"

	"github.com/winebarrel/myschema/model"
	"github.com/winebarrel/myschema/parser"
	"github.com/winebarrel/orderedmap"
)

// TableDiffResult separates FK operations from other statements so callers
// can order them: table renames first, then FK drops, then table /
// column / constraint / index changes, then FK adds last.
type TableDiffResult struct {
	// RenameStmts holds `ALTER TABLE … RENAME TO …` statements, which
	// must execute *before* any FK drops or other ALTER TABLE statements
	// targeting the new name. If a table both renames and has an FK
	// changed, the FK-drop on the new name would fire against a table
	// that doesn't exist yet under that name without this ordering.
	RenameStmts         []string
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
	// without false negatives. Renames go in their own bucket so the
	// caller can sequence them ahead of FK drops on the (now-renamed)
	// target table.
	renameStmts, err := applyTableRenames(current, desired)
	if err != nil {
		return nil, err
	}
	res.RenameStmts = append(res.RenameStmts, renameStmts...)

	// New tables: emit CREATE TABLE, then per-table secondary indexes and FKs.
	for k, dt := range desired.All() {
		if _, ok := current.GetOk(k); ok {
			continue
		}
		// Same MySQL rules the diffTable path enforces on the
		// modified-table side: (1) LIST `VALUES IN (…)`
		// constants must be disjoint across partitions, (2)
		// RANGE `VALUES LESS THAN` sequences must be strictly
		// increasing, and (3) every unique key on a
		// partitioned table must include all columns in the
		// partition expression. All three would otherwise
		// surface as MySQL errors at apply time (or, worse,
		// the CREATE accepted and a follow-up rejected). Run
		// them at plan time so the operator gets the same
		// actionable message regardless of whether the table
		// is brand-new or already exists.
		if dt.Partition != nil {
			desPO, err := parser.ParsePartitionClause(*dt.Partition)
			if err != nil {
				return nil, fmt.Errorf("table %s: re-parse desired partition clause: %w", k, err)
			}
			if err := validateDesiredListValuesAreDisjoint(k, desPO.Definitions); err != nil {
				return nil, err
			}
			if err := validateDesiredRangeMonotonic(k, desPO.Definitions); err != nil {
				return nil, err
			}
			required, err := partitionRequiredColumns(*dt.Partition)
			if err != nil {
				return nil, fmt.Errorf("table %s: re-parse desired partition clause: %w", k, err)
			}
			if missing := uniqueKeyPartitionCoverGap(dt, required); missing != "" {
				return nil, fmt.Errorf("table %s: %s — MySQL requires every unique key (including the PRIMARY KEY) on a partitioned table to include all columns in the partition expression. Either add the missing columns to the unique key, or remove `PARTITION BY …` from the desired CREATE TABLE", k, missing)
			}
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
		// Map key `k` is the FQTN ("db.name") — unqualify for SQL
		// emission. (FQTN itself stays db-qualified for map keys
		// and error context elsewhere.)
		tableIdent := model.Ident(ct.Name)
		if !tableAllowed {
			for name := range ct.ForeignKeys.Keys() {
				res.DisallowedDropStmts = append(res.DisallowedDropStmts,
					"-- skipped: ALTER TABLE "+tableIdent+" DROP FOREIGN KEY "+model.Ident(name)+";")
			}
			res.DisallowedDropStmts = append(res.DisallowedDropStmts, "-- skipped: DROP TABLE "+tableIdent+";")
			continue
		}
		for name := range ct.ForeignKeys.Keys() {
			res.FKDropStmts = append(res.FKDropStmts,
				"ALTER TABLE "+tableIdent+" DROP FOREIGN KEY "+model.Ident(name)+";")
		}
		res.DropStmts = append(res.DropStmts, "DROP TABLE "+tableIdent+";")
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
	// Unqualified for SQL emission (myschema operates on one DB per
	// invocation; the qualifier would be noise on every ALTER TABLE).
	// FQTN itself stays db-qualified — it's still used for map keys
	// and error context.
	tableIdent := model.Ident(desired.Name)

	// Column rename pass first, so the index-rename pass below and the
	// regular column / index / FK diffs see the renamed objects under
	// their new names. Index parts AND FK column lists that referenced
	// the old column names are rewritten in place (current side only)
	// so indexEqual / fkEqual stay quiet for objects that should NOT
	// trigger a DROP+CREATE / DROP+ADD.
	renames := columnRenameMap(desired.Columns)
	colRenameStmts, err := applyColumnRenames(tableIdent, current.Columns, desired.Columns)
	if err != nil {
		return nil, err
	}
	res.Stmts = append(res.Stmts, colRenameStmts...)
	rewriteIndexColumnRefs(current.Indexes, renames)
	rewriteFKColumnRefs(current.ForeignKeys, current.Database, current.Name, renames)
	rewriteConstraintColumnRefs(current.Constraints, current.Indexes, renames)
	idxRenameStmts, err := applyIndexRenames(tableIdent, current.Indexes, desired.Indexes)
	if err != nil {
		return nil, err
	}
	res.Stmts = append(res.Stmts, idxRenameStmts...)

	// CHECK constraint and FK rename directives are typo-guards only —
	// MySQL has no in-place RENAME CONSTRAINT / RENAME FOREIGN KEY, so
	// the diff still emits DROP+ADD via diffConstraints / diffForeignKeys
	// below. Validating up front means a typo'd source name aborts the
	// plan instead of silently dropping + adding the wrong target.
	if err := validateConstraintRenames(tableIdent, current.Constraints, desired.Constraints); err != nil {
		return nil, err
	}
	if err := validateForeignKeyRenames(tableIdent, current.ForeignKeys, desired.ForeignKeys); err != nil {
		return nil, err
	}

	// Partition diff: see PARTITIONING.md for the supported shapes
	// (suffix add, subset drop, HASH/KEY count change,
	// REORGANIZE-based per-partition definition rewrite) and the
	// shapes that still error out (split / merge / reorder,
	// strategy / expression change, first-time PARTITION BY,
	// REMOVE PARTITIONING, SUBPARTITION). Run before the column /
	// index passes so the user sees the partition error immediately.
	partStmts, partDisallowed, err := diffPartitions(tableIdent, current.Partition, desired.Partition, dc)
	if err != nil {
		return nil, err
	}
	// Partitioned-table unique-key cover guard. MySQL requires
	// "every unique key (including the PRIMARY KEY) must include
	// all columns in the table's partitioning function" — without
	// it, a desired schema that omits a partition column from a
	// unique key (or strips the partition column from PRIMARY KEY
	// without dropping partitioning too) would generate ADD
	// PRIMARY KEY / ADD UNIQUE INDEX statements MySQL rejects.
	// Run after diffPartitions so the more-specific partition
	// errors take precedence; only when desired stays partitioned.
	if desired.Partition != nil {
		required, err := partitionRequiredColumns(*desired.Partition)
		if err != nil {
			return nil, fmt.Errorf("table %s: re-parse desired partition clause: %w", tableIdent, err)
		}
		if missing := uniqueKeyPartitionCoverGap(desired, required); missing != "" {
			return nil, fmt.Errorf("table %s: %s — MySQL requires every unique key (including the PRIMARY KEY) on a partitioned table to include all columns in the partition expression. Either add the missing columns to the unique key, or drop partitioning first (REMOVE PARTITIONING by hand) and let the next plan reconverge", tableIdent, missing)
		}
	}
	res.Stmts = append(res.Stmts, partStmts...)
	res.DisallowedDropStmts = append(res.DisallowedDropStmts, partDisallowed...)

	// Table-level CHARACTER SET / COLLATE diff. Emitted before the
	// column pass so that any per-column charset rewrite below sees the
	// new table default (catalog-side column normalisation collapses a
	// column-level value that matches the table default to nil, so
	// changing the table default is functionally equivalent to changing
	// every "inherited" column). Engine is intentionally not diffed
	// here yet.
	if charsetStmt := tableCharsetCollationSQL(tableIdent, current, desired); charsetStmt != "" {
		res.Stmts = append(res.Stmts, charsetStmt)
	}

	// Table-level COMMENT diff. Order doesn't interact with the
	// column pass below — COMMENT is metadata only, no row rewrite —
	// so it sits next to the charset branch for symmetry.
	if commentStmt := tableCommentSQL(tableIdent, current, desired); commentStmt != "" {
		res.Stmts = append(res.Stmts, commentStmt)
	}

	colStmts, colDisallowed := diffColumns(tableIdent, current.Columns, desired.Columns, dc)
	res.Stmts = append(res.Stmts, colStmts...)
	res.DisallowedDropStmts = append(res.DisallowedDropStmts, colDisallowed...)

	conStmts, conDisallowed := diffConstraints(tableIdent, current.Constraints, desired.Constraints, dc)
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
	idxStmts, idxDisallowed := diffIndexes(tableIdent, current.Indexes, desired.Indexes, dc, dropped)
	res.Stmts = append(res.Stmts, idxStmts...)
	res.DisallowedDropStmts = append(res.DisallowedDropStmts, idxDisallowed...)

	fkDrops, fkAdds, fkDisallowed := diffForeignKeys(tableIdent, current.ForeignKeys, desired.ForeignKeys, dc)
	res.FKDropStmts = append(res.FKDropStmts, fkDrops...)
	res.FKAddStmts = append(res.FKAddStmts, fkAdds...)
	res.DisallowedDropStmts = append(res.DisallowedDropStmts, fkDisallowed...)

	return res, nil
}

// uniqueKeyPartitionCoverGap reports the first unique key on the
// desired-side table whose columns don't cover every entry in
// `required`. Returns "" when all unique keys (PRIMARY KEY plus
// any UNIQUE index) include the partition columns. Comparison is
// case-insensitive (column-name case in MySQL is, and required
// has already been lower-cased by partitionRequiredColumns).
//
// PRIMARY KEY is modelled as `Index{Primary: true, Name:
// "PRIMARY"}` — same code path as UNIQUE indexes here, no
// separate Constraint walk needed.
func uniqueKeyPartitionCoverGap(t *model.Table, required []string) string {
	if len(required) == 0 {
		return ""
	}
	for _, idx := range t.Indexes.CollectValues() {
		if !idx.Primary && idx.KeyType != model.IndexUnique {
			continue
		}
		have := make(map[string]bool, len(idx.Parts))
		for _, p := range idx.Parts {
			have[strings.ToLower(p.Column)] = true
		}
		var missing []string
		for _, c := range required {
			if !have[c] {
				missing = append(missing, c)
			}
		}
		if len(missing) > 0 {
			label := "unique key " + idx.Name
			if idx.Primary {
				label = "PRIMARY KEY"
			}
			return label + " is missing partition column(s) " + fmt.Sprintf("%v", missing)
		}
	}
	return ""
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

func diffColumns(tableIdent string, current, desired *orderedmap.Map[string, *model.Column], dc DropChecker) (stmts, disallowed []string) {
	colAllowed := dc.IsDropAllowed("column")

	// New / changed columns. New columns get a positional clause
	// (`AFTER <prev>` or `FIRST`) derived from desired SQL's column
	// order, so MySQL inserts them where the user expected instead of
	// silently appending to the end of the row.
	//
	// `anchor` is the desired-column-order predecessor of whatever
	// column we're looking at: it's updated for *every* desired column
	// (existing or newly-added) so a run of consecutive new columns
	// chains AFTER each preceding new column, instead of all stacking
	// AFTER the last existing one (which would reverse their order on
	// apply). For the very first desired column the anchor is empty,
	// which becomes FIRST.
	var anchor string
	for name, dc2 := range desired.All() {
		cc, ok := current.GetOk(name)
		if !ok {
			stmts = append(stmts, addColumnSQL(tableIdent, dc2, anchor))
			anchor = name
			continue
		}
		anchor = name
		if !columnEqual(cc, dc2) {
			stmts = append(stmts, modifyColumnSQL(tableIdent, dc2))
		}
	}
	// Dropped columns
	for _, name := range droppedColumns(current, desired) {
		drop := "ALTER TABLE " + tableIdent + " DROP COLUMN " + model.Ident(name) + ";"
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
	// Column-level CHARACTER SET / COLLATE. catalog/loadColumns
	// normalises a column-level value that matches the table default to
	// nil, so an "inherited" column compares equal to a parser-side
	// column that the user didn't spell out. Explicit per-column
	// overrides (or true mismatches) survive normalisation and surface
	// here as MODIFY COLUMN.
	if !ptrEq(a.CharacterSet, b.CharacterSet) {
		return false
	}
	if !ptrEq(a.Collation, b.Collation) {
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

// tableCharsetCollationSQL returns an ALTER TABLE that updates the
// table's default CHARACTER SET / COLLATE when the desired side
// differs from the current side. Returns "" when both sides agree
// OR when desired specifies neither (catalog will be carrying a
// server-default charset; the user is opting in to "whatever MySQL
// gives me", so don't fight it).
//
// Comparison semantics:
//
//   - desired.Charset == nil → "the user didn't declare a charset, so
//     match whatever catalog has". Lets a desired-only `COLLATE=…`
//     converge against any catalog charset.
//   - desired.Collation == nil with desired.Charset != nil → "use the
//     charset's default collation". Catalog always collapses the
//     default down to nil after CollapseDefaultCollation, so the two
//     sides agree iff current.Collation is also nil; if catalog
//     carries an explicit non-default collation the diff DOES emit
//     an ALTER (`DEFAULT CHARSET=…`) so MySQL resets the table
//     collation to that charset's default.
//   - desired.Collation == nil with desired.Charset == nil → handled
//     by the early return; nothing to compare.
//   - both sides set → strict ptrEq.
//
// The emitted DDL spells out only the clauses the desired side
// actually set: `ALTER TABLE … DEFAULT CHARSET=…` when only Charset
// changes, `ALTER TABLE … COLLATE=…` when only Collation changes,
// and `ALTER TABLE … DEFAULT CHARSET=… COLLATE=…` when both do. In
// any of these default shapes the statement only changes the table
// default; it does NOT rewrite existing column data, so a per-column
// MODIFY follows in a second apply (the two-stage convergence
// documented in CAVEATS.md "Changing DEFAULT CHARSET").
//
// `-- myschema:convert-charset` opts into the data-rewriting form:
// when set on the desired-side CREATE TABLE *and* the charset
// actually differs, the function emits `ALTER TABLE … CONVERT TO
// CHARACTER SET <charset> [COLLATE <collation>]` instead, which
// rewrites stored bytes and per-column charset metadata in the same
// statement (one-shot convergence). A collation-only diff with the
// directive set still falls through to the default DEFAULT CHARSET
// / COLLATE branch (no CONVERT) — and because the parser requires
// the desired CREATE TABLE to declare `DEFAULT CHARSET` for the
// directive to be valid, the fallback emits the full `DEFAULT
// CHARSET=<charset> COLLATE=<collation>` shape, never a bare
// `COLLATE=` only. CONVERT TO would needlessly rebuild the table
// just to flip metadata.
//
// Per-column drift is picked up by the column diff below; the
// catalog-side normalisation of column charset/collation against
// the new default keeps that comparison honest.
func tableCharsetCollationSQL(tableIdent string, current, desired *model.Table) string {
	if desired.Charset == nil && desired.Collation == nil {
		return ""
	}
	charsetMatches := desired.Charset == nil || ptrEq(current.Charset, desired.Charset)
	var collationMatches bool
	switch {
	case desired.Collation != nil:
		collationMatches = ptrEq(current.Collation, desired.Collation)
	case desired.Charset != nil:
		// Desired says "use the default collation for this charset" —
		// catalog represents that as nil after CollapseDefaultCollation.
		collationMatches = current.Collation == nil
	default:
		// Both nil; covered by the early return above.
		collationMatches = true
	}
	if charsetMatches && collationMatches {
		return ""
	}
	var b strings.Builder
	b.WriteString("ALTER TABLE ")
	b.WriteString(tableIdent)
	// `-- myschema:convert-charset` opt-in: rewrite stored bytes
	// and per-column charset metadata in one statement so a
	// table with pre-existing string columns converges in a
	// single apply (the default flow needs two — see CAVEATS
	// "Changing DEFAULT CHARSET"). Only triggered when the
	// charset itself differs — a collation-only diff falls
	// through to the default DEFAULT CHARSET / COLLATE branch
	// (which, because the parser requires DEFAULT CHARSET on
	// the directive's CREATE TABLE, emits the full `DEFAULT
	// CHARSET=<charset> COLLATE=<collation>` shape) because
	// `CONVERT TO` would needlessly rebuild the table just to
	// flip the collation. Note the syntax difference from the
	// default branch: CONVERT TO uses bare `COLLATE <name>`
	// (no `=`), DEFAULT CHARSET uses `COLLATE=<name>`.
	// desired.Charset is guaranteed non-nil here because the
	// parser rejects the directive when CREATE TABLE has no
	// DEFAULT CHARSET.
	if desired.ConvertCharset && !charsetMatches {
		b.WriteString(" CONVERT TO CHARACTER SET ")
		b.WriteString(*desired.Charset)
		if desired.Collation != nil {
			b.WriteString(" COLLATE ")
			b.WriteString(*desired.Collation)
		}
		b.WriteString(";")
		return b.String()
	}
	if desired.Charset != nil {
		b.WriteString(" DEFAULT CHARSET=")
		b.WriteString(*desired.Charset)
	}
	if desired.Collation != nil {
		b.WriteString(" COLLATE=")
		b.WriteString(*desired.Collation)
	}
	b.WriteString(";")
	return b.String()
}

// tableCommentSQL returns an ALTER TABLE that updates the table's
// COMMENT clause when desired and current disagree. Returns "" when
// they match. Both sides treat the absence of a clause as nil (the
// catalog reader maps the empty TABLE_COMMENT to nil), and a
// desired-side explicit empty-string COMMENT (the user wrote
// `COMMENT=` with an empty literal) is folded into nil here too —
// MySQL stores both as the empty string, the catalog hands them
// back as nil, so without the fold the parser-side `&""` would
// never compare equal to the catalog-side `nil` and `plan` would
// re-emit `ALTER TABLE ... COMMENT=<empty>` every run.
//
// Removing a previously-set comment is emitted as
// `ALTER TABLE ... COMMENT=<empty literal>` — MySQL's only way to
// clear `TABLE_COMMENT`, since ALTER TABLE has no `DROP COMMENT`
// syntax. Catalog round-trips that empty string back to nil on the
// next read, so the change converges in one apply.
func tableCommentSQL(tableIdent string, current, desired *model.Table) string {
	cur := canonicalComment(current.Comment)
	des := canonicalComment(desired.Comment)
	if ptrEq(cur, des) {
		return ""
	}
	v := ""
	if des != nil {
		v = *des
	}
	return "ALTER TABLE " + tableIdent + " COMMENT=" + model.QuoteLiteral(v) + ";"
}

// canonicalComment folds `&""` to `nil` so an explicit empty
// COMMENT clause on the desired side compares equal to the
// catalog-side nil that empty TABLE_COMMENT round-trips to.
func canonicalComment(c *string) *string {
	if c == nil || *c == "" {
		return nil
	}
	return c
}

// addColumnSQL / modifyColumnSQL share model.ColumnDefSQL with CREATE TABLE
// so that ALTER also carries CHARACTER SET / COLLATE / GENERATED clauses.
//
// `afterCol` is the desired-side anchor column for ADD COLUMN's positional
// clause: pass the name of the column the new one should sit immediately
// after, or "" for `FIRST`. Empty anchor + zero existing columns is the
// degenerate "very first column" case and also gets FIRST — harmless on
// MySQL.
func addColumnSQL(tableIdent string, c *model.Column, afterCol string) string {
	pos := " FIRST"
	if afterCol != "" {
		pos = " AFTER " + model.Ident(afterCol)
	}
	return "ALTER TABLE " + tableIdent + " ADD COLUMN " + model.ColumnDefSQL(c) + pos + ";"
}

func modifyColumnSQL(tableIdent string, c *model.Column) string {
	return "ALTER TABLE " + tableIdent + " MODIFY COLUMN " + model.ColumnDefSQL(c) + ";"
}

func diffConstraints(tableIdent string, current, desired *orderedmap.Map[string, *model.Constraint], dc DropChecker) (stmts, disallowed []string) {
	conAllowed := dc.IsDropAllowed("constraint")

	for name, ccon := range current.All() {
		dcon, ok := desired.GetOk(name)
		if ok && constraintEqual(ccon, dcon) {
			continue
		}
		// PK uses DROP PRIMARY KEY, not DROP CONSTRAINT.
		drop := dropConstraintSQL(tableIdent, ccon)
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
		stmts = append(stmts, addConstraintSQL(tableIdent, dcon))
	}
	return
}

func dropConstraintSQL(tableIdent string, c *model.Constraint) string {
	switch c.Type {
	case model.PrimaryKeyConstraint:
		return "ALTER TABLE " + tableIdent + " DROP PRIMARY KEY;"
	default:
		return "ALTER TABLE " + tableIdent + " DROP CHECK " + model.Ident(c.Name) + ";"
	}
}

func addConstraintSQL(tableIdent string, c *model.Constraint) string {
	switch c.Type {
	case model.PrimaryKeyConstraint:
		return "ALTER TABLE " + tableIdent + " ADD PRIMARY KEY " + c.Definition + ";"
	case model.CheckConstraint:
		return "ALTER TABLE " + tableIdent + " ADD CONSTRAINT " + model.Ident(c.Name) + " " + c.Definition + ";"
	}
	return "ALTER TABLE " + tableIdent + " ADD CONSTRAINT " + model.Ident(c.Name) + " " + c.Definition + ";"
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

func diffIndexes(tableIdent string, current, desired *orderedmap.Map[string, *model.Index], dc DropChecker, droppedCols map[string]bool) (stmts, disallowed []string) {
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
		drop := "ALTER TABLE " + tableIdent + " DROP INDEX " + model.Ident(name) + ";"
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
	if !slices.Equal(a.Parts, b.Parts) {
		return false
	}
	if normalizeIndexType(a.IndexType) != normalizeIndexType(b.IndexType) {
		return false
	}
	if a.Invisible != b.Invisible {
		return false
	}
	if !ptrEq(a.Comment, b.Comment) {
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

func diffForeignKeys(tableIdent string, current, desired *orderedmap.Map[string, *model.ForeignKey], dc DropChecker) (drops, adds, disallowed []string) {
	fkAllowed := dc.IsDropAllowed("foreign_key")

	for name, cf := range current.All() {
		df, ok := desired.GetOk(name)
		if ok && fkEqual(cf, df) {
			continue
		}
		drop := "ALTER TABLE " + tableIdent + " DROP FOREIGN KEY " + model.Ident(name) + ";"
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
	if !slices.Equal(a.Columns, b.Columns) || !slices.Equal(a.RefCols, b.RefCols) {
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
