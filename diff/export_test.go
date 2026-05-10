package diff

// AllowList is a DropChecker that allows only the listed kinds. Lives in
// _test.go so it doesn't ship in the production binary — production
// callers pass *Options.DropPolicy from the root package, which has its
// own IsDropAllowed. The token "all" matches every kind.
type AllowList struct {
	Kinds map[string]bool
}

func (a *AllowList) IsDropAllowed(kind string) bool {
	if a == nil {
		return false
	}
	if a.Kinds["all"] {
		return true
	}
	return a.Kinds[kind]
}

// Re-export unexported helpers for external test packages so behaviour
// can be pinned without polluting the public API.

var (
	PtrEq                      = ptrEq[string]
	LooseEqual                 = looseEqual
	NormalizeIndexType         = normalizeIndexType
	ColumnEqual                = columnEqual
	ConstraintEqual            = constraintEqual
	IndexEqual                 = indexEqual
	FKEqual                    = fkEqual
	AllPartsDropped            = allPartsDropped
	SplitCheckDef              = splitCheckDef
	CanonicalExpr              = canonicalExpr
	EqualExpr                  = equalExpr
	ViewDefEqual               = viewDefEqual
	TopoSortViews              = topoSortViews
	DropConstraintSQL          = dropConstraintSQL
	AddConstraintSQL           = addConstraintSQL
	TemporarilySortListValues  = temporarilySortListValues
	PartitionValueRangeEqual   = partitionValueRangeEqual
	PartitionHeaderEqual       = partitionHeaderEqual
	RangeBoundaryAsInt64       = rangeBoundaryAsInt64
	TableCharsetCollationSQL   = tableCharsetCollationSQL
	TableCommentSQL            = tableCommentSQL
	CanonicalComment           = canonicalComment
	UniqueKeyPartitionCoverGap = uniqueKeyPartitionCoverGap
	PartitionRequiredColumns   = partitionRequiredColumns
	NormalizeDropChecker       = normalizeDropChecker
)
