package parser

import "vitess.io/vitess/go/vt/sqlparser"

// Re-export unexported helpers for external test packages so behaviour
// can be pinned without polluting the public API.

var (
	AutoCheckName        = autoCheckName
	AutoFKName           = autoFKName
	DBName               = dbName
	NormalizeDefaultExpr = normalizeDefaultExpr
	RestoreSelectLower   = restoreSelectLower
)

func ReferenceActionString(a sqlparser.ReferenceAction) string {
	return referenceActionString(a)
}
