package model

import (
	"strconv"
	"strings"
)

// IndexKeyType reflects the optional CREATE INDEX keyword (UNIQUE / FULLTEXT /
// SPATIAL). Empty means a plain INDEX.
type IndexKeyType string

const (
	IndexPlain    IndexKeyType = ""
	IndexUnique   IndexKeyType = "UNIQUE"
	IndexFulltext IndexKeyType = "FULLTEXT"
	IndexSpatial  IndexKeyType = "SPATIAL"
)

// IndexPart is one entry in the index column list. Either Column or Expr is set.
type IndexPart struct {
	Column string
	Expr   string // wrapped expression (without surrounding parens)
	Length int    // 0 means full
	Desc   bool
}

func (p IndexPart) SQL() string {
	if p.Expr != "" {
		s := "(" + p.Expr + ")"
		if p.Desc {
			s += " DESC"
		}
		return s
	}
	s := Ident(p.Column)
	if p.Length > 0 {
		s += "(" + strconv.Itoa(p.Length) + ")"
	}
	if p.Desc {
		s += " DESC"
	}
	return s
}

// Index represents a secondary or primary key index. The PRIMARY KEY is also
// recorded here to make column-prefix and ordering changes diffable.
type Index struct {
	Name      string
	Database  string
	Table     string
	KeyType   IndexKeyType
	Parts     []IndexPart
	IndexType string // BTREE / HASH (empty when unspecified)
	Comment   *string
	Invisible bool
	Primary   bool // PRIMARY KEY (modelled as Index, name = "PRIMARY")
	// RenameFrom: previous index name from a `-- myschema:renamed-from`
	// inline directive. Drives ALTER TABLE … RENAME INDEX. Always nil on
	// the catalog side.
	RenameFrom *string
}

// SQL emits a standalone CREATE INDEX (or for PRIMARY KEY a constraint ALTER).
// Primary keys are emitted via Constraint, never via this method.
func (i *Index) SQL() string {
	var b strings.Builder
	b.WriteString("CREATE ")
	if i.KeyType != "" && i.KeyType != IndexPlain {
		b.WriteString(string(i.KeyType))
		b.WriteString(" ")
	}
	b.WriteString("INDEX ")
	b.WriteString(Ident(i.Name))
	b.WriteString(" ON ")
	b.WriteString(Ident(i.Database, i.Table))
	b.WriteString(" (")
	for k, p := range i.Parts {
		if k > 0 {
			b.WriteString(", ")
		}
		b.WriteString(p.SQL())
	}
	b.WriteString(")")
	if i.IndexType != "" {
		b.WriteString(" USING ")
		b.WriteString(i.IndexType)
	}
	if i.Invisible {
		b.WriteString(" INVISIBLE")
	}
	if i.Comment != nil {
		b.WriteString(" COMMENT ")
		b.WriteString(QuoteLiteral(*i.Comment))
	}
	b.WriteString(";")
	return b.String()
}
