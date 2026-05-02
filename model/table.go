package model

import (
	"strings"

	"github.com/winebarrel/orderedmap"
)

// Table is the canonical table definition shared between the parser and the
// catalog reader. Indexes/Constraints/ForeignKeys are kept as ordered maps so
// the serialized output is deterministic.
type Table struct {
	Database    string
	Name        string
	Engine      *string
	Charset     *string
	Collation   *string
	Comment     *string
	AutoIncrement *uint64
	Columns     *orderedmap.Map[string, *Column]
	Constraints *orderedmap.Map[string, *Constraint] // PK / UNIQUE / CHECK; UNIQUE indexes are NOT here, they live in Indexes
	ForeignKeys *orderedmap.Map[string, *ForeignKey]
	Indexes     *orderedmap.Map[string, *Index]
}

// FQTN returns the database-qualified name (database.table).
func (t *Table) FQTN() string {
	return Ident(t.Database, t.Name)
}

// SQL renders an inline CREATE TABLE that declares columns and inline
// table-level constraints (PK/UNIQUE/CHECK). Foreign keys and secondary
// indexes are emitted separately by IdxSQL/FkSQL so apply/diff can order them.
func (t *Table) SQL() string {
	var b strings.Builder
	b.WriteString("CREATE TABLE ")
	b.WriteString(t.FQTN())
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
	b.WriteString(";")
	return b.String()
}

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
	case UniqueConstraint:
		return "CONSTRAINT " + Ident(con.Name) + " UNIQUE " + con.Definition
	case CheckConstraint:
		return "CONSTRAINT " + Ident(con.Name) + " CHECK " + con.Definition
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
	parts := []string{"-- " + t.FQTN(), t.SQL()}
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
