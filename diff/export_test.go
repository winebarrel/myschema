package diff

// Re-export unexported helpers for external test packages so behaviour
// can be pinned without polluting the public API.

var (
	PtrEq                     = ptrEq[string]
	LooseEqual                = looseEqual
	NormalizeIndexTyp         = normalizeIndexType
	ColumnEqual               = columnEqual
	ConstraintEqual           = constraintEqual
	IndexEqual                = indexEqual
	FKEqual                   = fkEqual
	AllPartsDropped           = allPartsDropped
	SplitCheckDef             = splitCheckDef
	CanonicalExpr             = canonicalExpr
	EqualExpr                 = equalExpr
	ViewDefEqual              = viewDefEqual
	TopoSortViews             = topoSortViews
	DropConstraintSQL         = dropConstraintSQL
	AddConstraintSQL          = addConstraintSQL
	TemporarilySortListValues = temporarilySortListValues
	PartitionValueRangeEqual  = partitionValueRangeEqual
	PartitionHeaderEqual      = partitionHeaderEqual
	RangeBoundaryAsInt64      = rangeBoundaryAsInt64
)
