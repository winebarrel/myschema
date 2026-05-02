package model

// ConstraintType matches the kinds we model. MySQL has no separate EXCLUSION,
// and KEY/INDEX are modelled as Index, not Constraint.
type ConstraintType byte

const (
	PrimaryKeyConstraint ConstraintType = 'p'
	UniqueConstraint     ConstraintType = 'u'
	CheckConstraint      ConstraintType = 'c'
	ForeignKeyConstraint ConstraintType = 'f'
)

// Constraint covers PRIMARY KEY, UNIQUE, and CHECK.
// Foreign keys live in their own struct because they carry a reference target.
type Constraint struct {
	Name       string
	Type       ConstraintType
	Definition string   // post-parenthesis body, e.g. `(a, b)` or `CHECK (x > 0)`
	Columns    []string // empty for CHECK
	Enforced   bool     // CHECK only
}
