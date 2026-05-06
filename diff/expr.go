package diff

import (
	"strings"

	"vitess.io/vitess/go/vt/sqlparser"
)

// canonicalExpr parses s as a SQL expression and returns vitess's
// canonical string form. Used to compare DEFAULT, ON UPDATE, CHECK,
// and GENERATED expressions across the parser side and the catalog
// side, neither of which produce byte-identical output for
// semantically equal SQL.
//
// Charset introducers (`_utf8mb4'foo'`, `_latin1'bar'`) on string
// literals are stripped before comparison. MySQL canonicalises
// generated-column bodies by prefixing every string literal with the
// session's `character_set_connection` introducer at CREATE time, so
// a desired-side body like `CONCAT(a, ' ', b)` round-trips on the
// catalog side as `concat(a, _utf8mb4' ', b)` and the diff would
// otherwise fire `MODIFY COLUMN` on every plan. Stripping the
// introducer from both sides closes the loop.
//
// **Scope.** The strip applies to *every* expression comparison
// that flows through this helper — DEFAULT, ON UPDATE, CHECK, and
// GENERATED — because they all share `canonicalExpr`. A deliberate
// introducer-only change in any of those slots (e.g.
// `DEFAULT _latin1'foo'` → `DEFAULT _utf8mb4'foo'`) is therefore
// invisible to the diff. The trade-off is documented in CAVEATS.md
// "Generated column expression bodies that contain string literals"
// → "charset-introducer-only changes are invisible across the
// entire diff."
//
// Returns the original string and false if parsing fails so callers
// can fall back to byte equality.
func canonicalExpr(s string) (string, bool) {
	p, err := sqlparser.New(sqlparser.Options{})
	if err != nil {
		return s, false
	}
	stmt, err := p.Parse("SELECT " + s)
	if err != nil {
		return s, false
	}
	sel, ok := stmt.(*sqlparser.Select)
	if !ok || sel.SelectExprs == nil || len(sel.SelectExprs.Exprs) != 1 {
		return s, false
	}
	ae, ok := sel.SelectExprs.Exprs[0].(*sqlparser.AliasedExpr)
	if !ok {
		return s, false
	}
	stripped, ok := stripIntroducers(ae.Expr)
	if !ok {
		return s, false
	}
	return lowerOutsideStringLiterals(sqlparser.String(stripped)), true
}

// lowerOutsideStringLiterals lower-cases every byte except the
// content of single-quoted string literals. Naïve `strings.ToLower`
// would canonicalise `DEFAULT 'X'` and `DEFAULT 'x'` to the same
// string, hiding a real case-sensitive value change. Function names,
// keywords, and unquoted identifiers still need lowering because
// vitess preserves their case from the input. The state machine
// tracks the in-literal flag and respects both backslash escapes
// (`\'`) and the SQL-standard doubled-apostrophe escape (two
// consecutive `'` characters meaning one literal apostrophe) so
// the literal boundaries are recognised correctly.
func lowerOutsideStringLiterals(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	i := 0
	for i < len(s) {
		c := s[i]
		if c != '\'' {
			if c >= 'A' && c <= 'Z' {
				c += 'a' - 'A'
			}
			b.WriteByte(c)
			i++
			continue
		}
		// Opening quote — copy verbatim and walk to the matching close.
		b.WriteByte(c)
		i++
		for i < len(s) {
			c2 := s[i]
			b.WriteByte(c2)
			i++
			if c2 == '\\' && i < len(s) {
				// Backslash escape — copy the escaped byte verbatim.
				b.WriteByte(s[i])
				i++
				continue
			}
			if c2 == '\'' {
				// Either closing quote or a doubled-quote escape (`''`).
				if i < len(s) && s[i] == '\'' {
					b.WriteByte(s[i])
					i++
					continue
				}
				break
			}
		}
	}
	return b.String()
}

// stripIntroducers walks the expression and replaces every
// `*sqlparser.IntroducerExpr` with its inner expression so the charset
// introducer doesn't survive into the canonical string. Returns
// (rewritten, true) on success, falling back to (original, false) only
// if the rewrite somehow returns a non-Expr — defensive.
func stripIntroducers(expr sqlparser.Expr) (sqlparser.Expr, bool) {
	rewritten := sqlparser.Rewrite(expr, func(c *sqlparser.Cursor) bool {
		if intro, ok := c.Node().(*sqlparser.IntroducerExpr); ok {
			c.Replace(intro.Expr)
		}
		return true
	}, nil)
	out, ok := rewritten.(sqlparser.Expr)
	if !ok {
		return expr, false
	}
	return out, true
}

// equalExpr compares two SQL expression strings semantically by passing
// both through vitess's parse → restore pipeline. Falls back to byte
// equality when either side fails to parse.
func equalExpr(a, b string) bool {
	if a == b {
		return true
	}
	na, okA := canonicalExpr(a)
	nb, okB := canonicalExpr(b)
	if !okA || !okB {
		return a == b
	}
	return na == nb
}

// equalExprPtr is the *string companion to equalExpr — both nil means
// equal, one nil means not equal, and otherwise the strings are compared
// after canonicalisation.
func equalExprPtr(a, b *string) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return equalExpr(*a, *b)
}

// equalCheckDef compares two CHECK constraint definitions of the form
// `CHECK (<expr>) [NOT ENFORCED]`. We normalise the expression part
// through canonicalExpr; the NOT ENFORCED suffix is compared
// case-insensitively after stripping whitespace.
func equalCheckDef(a, b string) bool {
	if a == b {
		return true
	}
	exprA, suffixA := splitCheckDef(a)
	exprB, suffixB := splitCheckDef(b)
	if !strings.EqualFold(suffixA, suffixB) {
		return false
	}
	return equalExpr(exprA, exprB)
}

// splitCheckDef breaks `CHECK (expr) NOT ENFORCED` into (expr, suffix).
// expr is the unwrapped expression body; suffix is whatever trails the
// closing paren (lowercased, whitespace-collapsed).
func splitCheckDef(def string) (expr, suffix string) {
	t := strings.TrimSpace(def)
	if !strings.HasPrefix(strings.ToUpper(t), "CHECK") {
		return def, ""
	}
	t = strings.TrimSpace(t[len("CHECK"):])
	if !strings.HasPrefix(t, "(") {
		return def, ""
	}
	// Find the matching close-paren by depth.
	depth := 0
	end := -1
	for i, r := range t {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				end = i
			}
		}
		if end >= 0 {
			break
		}
	}
	if end < 0 {
		return def, ""
	}
	expr = t[1:end]
	suffix = strings.ToLower(strings.Join(strings.Fields(t[end+1:]), " "))
	return expr, suffix
}
