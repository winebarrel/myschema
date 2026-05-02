package diff

import (
	"strings"

	"vitess.io/vitess/go/vt/sqlparser"
)

// canonicalExpr parses s as a SQL expression and returns vitess's
// canonical string form. Used to compare DEFAULT, ON UPDATE, and CHECK
// expressions across the parser side and the catalog side, neither of
// which produce byte-identical output for semantically equal SQL.
//
// Returns the original string and false if parsing fails so callers can
// fall back to byte equality.
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
	return strings.ToLower(sqlparser.String(ae.Expr)), true
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
