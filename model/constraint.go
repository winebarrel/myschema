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

// Constraint covers PRIMARY KEY, UNIQUE, and CHECK.
// Foreign keys live in their own struct because they carry a reference target.
type Constraint struct {
	Name       string
	Type       ConstraintType
	Definition string   // post-parenthesis body, e.g. `(a, b)` or `CHECK (x > 0)`
	Columns    []string // empty for CHECK
	Enforced   bool     // CHECK only
}
