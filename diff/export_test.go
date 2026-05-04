package diff

// Re-export unexported helpers for external test packages so behaviour
// can be pinned without polluting the public API.

var (
	PtrEq             = ptrEq[string]
	SliceEqString     = sliceEq[string]
	SliceEqInt        = sliceEq[int]
	LooseEqual        = looseEqual
	NormalizeIndexTyp = normalizeIndexType
	ColumnEqual       = columnEqual
	ConstraintEqual   = constraintEqual
	IndexEqual        = indexEqual
	FKEqual           = fkEqual
	AllPartsDropped   = allPartsDropped
	SplitCheckDef     = splitCheckDef
	CanonicalExpr     = canonicalExpr
	EqualExpr         = equalExpr
)
