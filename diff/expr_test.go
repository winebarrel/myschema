package diff

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEqualExpr(t *testing.T) {
	tests := []struct {
		name string
		a, b string
		want bool
	}{
		{"identical", "42", "42", true},
		{"different ints", "42", "43", false},
		{"current_timestamp variants", "CURRENT_TIMESTAMP", "current_timestamp()", true},
		{"current_timestamp with precision", "CURRENT_TIMESTAMP(6)", "current_timestamp(6)", true},
		{"now() variants", "now()", "NOW()", true},
		{"NULL", "NULL", "null", true},
		{"TRUE/FALSE", "TRUE", "true", true},
		{"quoted vs bareword", "'hello'", "hello", false},
		{"paren wrapping", "(1+2)", "1+2", true},
		{"spacing around operator", "a > 0", "a>0", true},
		{"unparseable falls back to byte equality", "garbage )(", "garbage )(", true},
		{"unparseable mismatch", "garbage )(", "different (", false},

		// Charset introducer stripping. MySQL's GENERATION_EXPRESSION
		// canonicalises every string literal in a generated body to
		// `_<connection_charset>'…'`; the parser side has no
		// introducer because the user wrote a plain literal. Without
		// the strip the diff would fire MODIFY COLUMN on every plan
		// for a no-op shape change.
		{
			"introducer absorbed (plain vs _utf8mb4)",
			"CONCAT(email, ' ', name)",
			"concat(`email`,_utf8mb4' ',`name`)",
			true,
		},
		{
			"introducer absorbed (plain vs _latin1)",
			"CONCAT(a, ' x ', b)",
			"concat(`a`,_latin1' x ',`b`)",
			true,
		},
		{
			"both sides explicit, same introducer → equal",
			"CONCAT(a, _latin1' x ', b)",
			"concat(`a`,_latin1' x ',`b`)",
			true,
		},
		{
			// Trade-off documented in CAVEATS — the strip makes a
			// deliberate `_latin1` → `_utf8mb4` literal-charset
			// change look identical. The diff sees this as no-op.
			"different introducers compare equal (documented limitation)",
			"CONCAT(a, _latin1' x ', b)",
			"concat(`a`,_utf8mb4' x ',`b`)",
			true,
		},
		{
			// Differing actual literal value still diffs even if
			// introducers cancel out.
			"different literal payload still diffs",
			"CONCAT(a, _utf8mb4' x ', b)",
			"concat(`a`,_utf8mb4' y ',`b`)",
			false,
		},
		{
			// Multiple string literals in one expression — strip
			// must walk the whole AST, not just the first literal.
			"multiple literals each with introducer",
			"CONCAT(a, ' / ', b, ' - ', c)",
			"concat(`a`,_utf8mb4' / ',`b`,_utf8mb4' - ',`c`)",
			true,
		},
		{
			// Nested function calls — introducers can sit at any
			// depth in the tree.
			"nested function call with introducer",
			"CONCAT(UPPER(CONCAT(a, ' ', b)), c)",
			"concat(upper(concat(`a`,_utf8mb4' ',`b`)),`c`)",
			true,
		},

		// String-literal case is part of the value, not the
		// canonical form. Earlier `canonicalExpr` lower-cased the
		// entire restored expression with `strings.ToLower`, which
		// canonicalised `'X'` and `'x'` to the same string and
		// hid real case-sensitive default changes.
		{
			"upper-case literal vs lower-case literal — different values",
			"'X'",
			"'x'",
			false,
		},
		{
			"function name case is canonicalised, literal case isn't",
			"CONCAT(a, 'X')",
			"concat(a, 'X')",
			true,
		},
		{
			"function name case canonicalised, literal mismatch survives",
			"CONCAT(a, 'X')",
			"concat(a, 'x')",
			false,
		},
		{
			// Backslash-escaped quote inside literal must not
			// confuse the literal-boundary tracking.
			"backslash escape inside literal",
			`CONCAT(a, 'O\'Brien')`,
			`concat(a, 'O\'Brien')`,
			true,
		},
		{
			// SQL-standard doubled-quote escape inside literal
			// must not be mistaken for a closing quote.
			"doubled-quote escape inside literal",
			`CONCAT(a, 'O''Brien')`,
			`concat(a, 'O''Brien')`,
			true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, equalExpr(tt.a, tt.b))
		})
	}
}

func TestEqualExprPtr(t *testing.T) {
	assert.True(t, equalExprPtr(nil, nil), "both nil")
	assert.False(t, equalExprPtr(new("x"), nil), "one nil")
	assert.False(t, equalExprPtr(nil, new("x")), "other nil")
	assert.True(t, equalExprPtr(new("CURRENT_TIMESTAMP"), new("current_timestamp()")), "canonicalised match")
}

func TestEqualCheckDef(t *testing.T) {
	tests := []struct {
		name string
		a, b string
		want bool
	}{
		{"identical", "CHECK (a > 0)", "CHECK (a > 0)", true},
		{"casing", "CHECK (a > 0)", "check (a > 0)", true},
		{"spacing in expr", "CHECK (a > 0)", "CHECK (a>0)", true},
		{"backticks in expr", "CHECK (`a` > 0)", "CHECK (a > 0)", true},
		{"different exprs", "CHECK (a > 0)", "CHECK (b > 0)", false},
		{"NOT ENFORCED suffix", "CHECK (a > 0) NOT ENFORCED", "CHECK (a > 0) not enforced", true},
		{"suffix differs", "CHECK (a > 0) NOT ENFORCED", "CHECK (a > 0) ENFORCED", false},

		// CHECK constraint expressions flow through canonicalExpr
		// just like generated bodies, so the introducer strip
		// applies here too. CAVEATS calls this out as the broader
		// scope of the trade-off — pin the behaviour with both a
		// "introducer absorbed" case and a "different introducers
		// equal by design" case.
		{
			"introducer absorbed (plain vs _utf8mb4)",
			"CHECK (status = 'open')",
			"CHECK (status = _utf8mb4'open')",
			true,
		},
		{
			"different introducers compare equal (documented limitation)",
			"CHECK (status = _latin1'open')",
			"CHECK (status = _utf8mb4'open')",
			true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, equalCheckDef(tt.a, tt.b))
		})
	}
}
