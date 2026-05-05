package model

import (
	"strings"

	"github.com/winebarrel/orderedmap"
)

// Table is the canonical table definition shared between the parser and the
// catalog reader. Indexes/Constraints/ForeignKeys are kept as ordered maps so
// the serialized output is deterministic.
type Table struct {
	Database      string
	Name          string
	Engine        *string
	Charset       *string
	Collation     *string
	Comment       *string
	AutoIncrement *uint64
	Columns       *orderedmap.Map[string, *Column]
	Constraints   *orderedmap.Map[string, *Constraint] // PK / UNIQUE / CHECK; UNIQUE indexes are NOT here, they live in Indexes
	ForeignKeys   *orderedmap.Map[string, *ForeignKey]
	Indexes       *orderedmap.Map[string, *Index]
	// RenameFrom is the previous name from a `-- myschema:renamed-from`
	// directive on the desired side. Catalog-loaded tables always have it
	// nil. The diff layer uses it to emit ALTER TABLE … RENAME TO instead
	// of DROP+CREATE so row data survives.
	RenameFrom *string
	// ConvertCharset is set by a desired-side
	// `-- myschema:convert-charset` directive (statement-level, above
	// the CREATE TABLE). When true and the table's CHARACTER SET differs
	// from the catalog, the diff layer emits a one-shot
	// `ALTER TABLE … CONVERT TO CHARACTER SET … [COLLATE …]` that
	// rewrites stored bytes and per-column charsets in the same
	// statement, instead of the default two-stage `DEFAULT CHARSET=…` +
	// per-column `MODIFY COLUMN` flow. Catalog-loaded tables always
	// have it false; the directive doesn't survive a dump/round-trip
	// because the catalog has no representation for "next apply should
	// CONVERT TO".
	ConvertCharset bool
	// Partition is the canonical PARTITION BY clause
	// (e.g. `partition by range (year(dt)) (partition p0 values less
	// than (2021))`). Both the parser and the catalog reader normalise
	// through `parser.NormalizePartitionOption`, which sits on top of
	// vitess's `sqlparser.String(*sqlparser.PartitionOption)` and adds
	// (a) lower-casing of function-name and column-reference
	// identifiers, (b) trimming of the leading newline vitess emits,
	// and (c) stripping of the per-partition `engine <name>` option
	// MySQL 8.0+ always includes in SHOW CREATE TABLE output. The two
	// sides therefore compare bytewise. nil = the table is not
	// partitioned. v1 only supports round-trip + drift detection: any
	// partition-side drift surfaces as a hard error rather than an
	// emitted ALTER, so users manage partition changes by hand for now.
	Partition *string
}

// FQTN returns the database-qualified name (database.table).
func (t *Table) FQTN() string {
	return Ident(t.Database, t.Name)
}

// SQL renders an inline CREATE TABLE that declares columns and inline
// table-level constraints (PK/UNIQUE/CHECK). Foreign keys and secondary
// indexes are emitted separately by IdxSQL/FkSQL so apply/diff can order them.
//
// The emitted statement is *unqualified* — `CREATE TABLE name`, not
// `CREATE TABLE db.name`. myschema operates on exactly one database
// per invocation (the DSN carries it), so the qualifier would be
// noise in every emitted line. FQTN itself still returns the
// db-qualified form because it's used for map keys and error
// messages.
func (t *Table) SQL() string {
	var b strings.Builder
	b.WriteString("CREATE TABLE ")
	b.WriteString(Ident(t.Name))
	b.WriteString(" (\n")

	first := true
	for _, col := range t.Columns.CollectValues() {
		if !first {
			b.WriteString(",\n")
		}
		first = false
		b.WriteString("    ")
		b.WriteString(columnDefSQL(col))
	}
	for _, con := range t.Constraints.CollectValues() {
		if !first {
			b.WriteString(",\n")
		}
		first = false
		b.WriteString("    ")
		b.WriteString(constraintInlineSQL(con))
	}
	b.WriteString("\n)")

	if t.Engine != nil {
		b.WriteString(" ENGINE=" + *t.Engine)
	}
	if t.Charset != nil {
		b.WriteString(" DEFAULT CHARSET=" + *t.Charset)
	}
	if t.Collation != nil {
		b.WriteString(" COLLATE=" + *t.Collation)
	}
	if t.Comment != nil {
		b.WriteString(" COMMENT=" + QuoteLiteral(*t.Comment))
	}
	if t.Partition != nil {
		// Partition is the canonical, vitess-formatted clause —
		// already lowercase, already wrapped in its own
		// `(partition … values …)` block when applicable.
		// Separate it from the table options with a newline so
		// dump output reads cleanly.
		b.WriteString("\n")
		b.WriteString(*t.Partition)
	}
	b.WriteString(";")
	return b.String()
}

// ColumnDefSQL renders the post-name body of a column definition
// (`<type> [CHARACTER SET …] [COLLATE …] [GENERATED …] [NOT NULL]
// [DEFAULT …] [ON UPDATE …] [AUTO_INCREMENT] [COMMENT …]`) prefixed by
// the column name formatted via Ident (back-tick quoted only when
// required by MySQL syntax). Used by `(*Table).SQL()` for the
// `CREATE TABLE` body and by `diff/tables.go` for `ALTER TABLE
// ADD/MODIFY COLUMN`, so both contexts emit the same set of attributes.
func ColumnDefSQL(col *Column) string { return columnDefSQL(col) }

func columnDefSQL(col *Column) string {
	var b strings.Builder
	b.WriteString(Ident(col.Name))
	b.WriteString(" ")
	b.WriteString(col.TypeName)
	if col.CharacterSet != nil {
		b.WriteString(" CHARACTER SET ")
		b.WriteString(*col.CharacterSet)
	}
	if col.Collation != nil {
		b.WriteString(" COLLATE ")
		b.WriteString(*col.Collation)
	}
	if col.Generated != nil {
		b.WriteString(" GENERATED ALWAYS AS (")
		b.WriteString(*col.Generated)
		b.WriteString(")")
		if col.Stored {
			b.WriteString(" STORED")
		} else {
			b.WriteString(" VIRTUAL")
		}
	}
	if col.NotNull {
		b.WriteString(" NOT NULL")
	}
	if col.Default != nil {
		b.WriteString(" DEFAULT ")
		b.WriteString(*col.Default)
	}
	if col.OnUpdate != nil {
		b.WriteString(" ON UPDATE ")
		b.WriteString(*col.OnUpdate)
	}
	if col.AutoIncrement {
		b.WriteString(" AUTO_INCREMENT")
	}
	// INVISIBLE goes after AUTO_INCREMENT and before COMMENT to match
	// MySQL's SHOW CREATE TABLE order. VISIBLE is the default and never
	// emitted; only the deviation surfaces.
	if col.Invisible {
		b.WriteString(" INVISIBLE")
	}
	if col.Comment != nil {
		b.WriteString(" COMMENT ")
		b.WriteString(QuoteLiteral(*col.Comment))
	}
	return b.String()
}

func constraintInlineSQL(con *Constraint) string {
	switch con.Type {
	case PrimaryKeyConstraint:
		return "PRIMARY KEY " + con.Definition
	case CheckConstraint:
		// con.Definition is already `CHECK (<expr>)` — see parser
		// addTableConstraint and catalog loadCheckConstraints. Don't
		// prepend another CHECK keyword. The optional `NOT ENFORCED`
		// suffix is appended here based on Enforced (parser stores
		// d.Enforced, catalog reads tc.ENFORCED).
		body := con.Definition
		if !con.Enforced {
			body += " NOT ENFORCED"
		}
		return "CONSTRAINT " + Ident(con.Name) + " " + body
	}
	return con.Definition
}

// IdxSQL returns CREATE INDEX statements for all secondary indexes (excluding
// the primary key, which is emitted inline in SQL()).
func (t *Table) IdxSQL() string {
	var stmts []string
	for _, idx := range t.Indexes.CollectValues() {
		if idx.Primary {
			continue
		}
		stmts = append(stmts, idx.SQL())
	}
	return strings.Join(stmts, "\n")
}

// FkSQL returns ALTER TABLE statements adding all FKs.
func (t *Table) FkSQL() string {
	var stmts []string
	for _, fk := range t.ForeignKeys.CollectValues() {
		stmts = append(stmts, fk.SQL())
	}
	return strings.Join(stmts, "\n")
}

// TableToSQL is the dump representation of a single table.
func TableToSQL(t *Table) string {
	parts := []string{"-- " + Ident(t.Name), t.SQL()}
	if s := t.IdxSQL(); s != "" {
		parts = append(parts, s)
	}
	if s := t.FkSQL(); s != "" {
		parts = append(parts, s)
	}
	return strings.Join(parts, "\n")
}

// TablesToSQL renders all tables in order, separated by blank lines.
func TablesToSQL(tables *orderedmap.Map[string, *Table]) string {
	parts := make([]string, 0, tables.Len())
	for _, t := range tables.CollectValues() {
		parts = append(parts, TableToSQL(t))
	}
	return strings.Join(parts, "\n\n")
}
