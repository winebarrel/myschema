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
}
