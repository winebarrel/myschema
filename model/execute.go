package model

// ExecuteGroup is a desired-side `-- myschema:execute <check-sql>`
// directive paired with the SQL statement that follows it. The check
// SQL is run at plan / apply time; if it returns zero rows the
// execute SQL is run (idempotent path), otherwise the execute is
// skipped. Used as the only sanctioned escape hatch for objects
// myschema doesn't model — triggers, stored procedures / functions,
// events, grants, ad-hoc DML, etc.
type ExecuteGroup struct {
	// CheckSQL is the post-directive `<check-sql>` body. The parser
	// strips the leading `-- myschema:execute` prefix but otherwise
	// passes the rest of the directive line through verbatim — any
	// trailing whitespace is trimmed by the regex, but a trailing
	// semicolon if the user wrote one is preserved (MySQL accepts
	// a single SELECT either way). Run with Query(); zero-row
	// result means "execute hasn't been applied yet, run it now".
	CheckSQL string
	// ExecuteSQL is the next statement after the directive line, kept
	// raw (no vitess parsing) because the typical contents
	// (CREATE TRIGGER, CREATE PROCEDURE, …) are outside vitess's
	// supported grammar. Trailing semicolon is preserved so apply can
	// hand it straight to the driver.
	ExecuteSQL string
}
