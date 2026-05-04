package catalog

// Re-export unexported helpers for external test packages so behaviour
// can be pinned without polluting the public API.

var (
	NormalizeColumnDefault             = normalizeColumnDefault
	ColumnTypeAllowsEmptyStringDefault = columnTypeAllowsEmptyStringDefault
	NullIfMatchesTableDefault          = nullIfMatchesTableDefault
	NormalizeRefOpt                    = normalizeRefOpt
	NormalizeMatch                     = normalizeMatch
)
