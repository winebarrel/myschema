package parser

import "vitess.io/vitess/go/vt/sqlparser"

// Re-export unexported helpers for external test packages so behaviour
// can be pinned without polluting the public API.

var (
	AutoCheckName                   = autoCheckName
	AutoFKName                      = autoFKName
	DBName                          = dbName
	NormalizeDefaultExpr            = normalizeDefaultExpr
	RestoreSelectLower              = restoreSelectLower
	EffectiveCharsetForCollation    = effectiveCharsetForCollation
	RejectMisplacedConvertCharset   = rejectMisplacedConvertCharset
	RejectMisplacedRenameDirectives = rejectMisplacedRenameDirectives
	StripUntilAfterBacktickedName   = stripUntilAfterBacktickedName
	ConsumeKeywordSequence          = consumeKeywordSequence
	LeadingBacktickedIdent          = leadingBacktickedIdent
	Tokenize                        = tokenize
)

// InlineKind constants (re-exported as ints so external tests can
// assert on them without depending on the unexported enum type).
const (
	InlineKindUnknown = int(inlineKindUnknown)
	InlineKindColumn  = int(inlineKindColumn)
	InlineKindIndex   = int(inlineKindIndex)
	InlineKindCheck   = int(inlineKindCheck)
	InlineKindFK      = int(inlineKindFK)
)

// ClassifyInlineLine wraps classifyInlineLine and returns the kind as
// an int so the unexported enum type doesn't leak.
func ClassifyInlineLine(line string) (int, string) {
	k, n := classifyInlineLine(line)
	return int(k), n
}

func ReferenceActionString(a sqlparser.ReferenceAction) string {
	return referenceActionString(a)
}

func ValidateExecuteCheckSQL(checkSQL string) error {
	p, err := newParser()
	if err != nil {
		return err
	}
	return validateExecuteCheckSQL(p, checkSQL)
}
