package model

// ConstraintType matches the kinds we model. MySQL has no separate EXCLUSION,
// and KEY/INDEX are modelled as Index, not Constraint.
type ConstraintType byte

const (
	PrimaryKeyConstraint ConstraintType = 'p'
	CheckConstraint      ConstraintType = 'c'
)

// ConstraintType also has 'u' (UNIQUE) and 'f' (FK) historically, but
// myschema models UNIQUE indexes via *Index and FKs via *ForeignKey, so
// neither value ever appears in a *Constraint produced by parser/catalog.
// They are intentionally not declared here to keep the type space honest.

// Constraint covers PRIMARY KEY and CHECK.
// UNIQUE constraints live in *Index (catalogued via
// information_schema.STATISTICS), and foreign keys live in *ForeignKey
// because they carry a reference target.
type Constraint struct {
	Name       string
	Type       ConstraintType
	Definition string   // post-parenthesis body, e.g. `(a, b)` or `CHECK (x > 0)`
	Columns    []string // empty for CHECK
	Enforced   bool     // CHECK only
	// RenameFrom: previous constraint name from a `-- myschema:renamed-from`
	// directive on the desired side. CHECK constraints only — PRIMARY KEY
	// has the fixed name "PRIMARY" and is never renamed via directive.
	// MySQL has no in-place RENAME CONSTRAINT, so this is consumed only
	// as a typo guard at plan time; the diff still emits DROP+ADD.
	RenameFrom *string
}
