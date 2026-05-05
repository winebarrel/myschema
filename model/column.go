package model

// Column models a MySQL column. The catalog reader and the SQL parser both
// populate the same struct so that the diff package can compare them by value.
type Column struct {
	Name          string
	TypeName      string // canonicalised, lowercase, includes length / unsigned / zerofill
	NotNull       bool
	Default       *string // raw expression as it would appear after DEFAULT
	OnUpdate      *string // ON UPDATE expression (TIMESTAMP/DATETIME)
	AutoIncrement bool
	Generated     *string // generation expression
	Stored        bool    // STORED vs VIRTUAL generated column
	Collation     *string
	CharacterSet  *string
	Comment       *string
	// Invisible mirrors MySQL 8.0+ `INVISIBLE` on a column. False means
	// the column is visible (the default — nothing emitted). The catalog
	// reader populates this from `information_schema.COLUMNS.EXTRA`,
	// which surfaces "INVISIBLE" alongside other extras.
	Invisible bool
	// RenameFrom: previous column name from a `-- myschema:renamed-from`
	// inline directive in CREATE TABLE. Drives ALTER TABLE … RENAME COLUMN
	// in the diff. Always nil on the catalog side.
	RenameFrom *string
}
