package diff_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/winebarrel/myschema/diff"
)

func TestSplitCheckDef(t *testing.T) {
	tests := []struct {
		name       string
		def        string
		wantExpr   string
		wantSuffix string
	}{
		{"basic", "CHECK (a > 0)", "a > 0", ""},
		{"with NOT ENFORCED", "CHECK (a > 0) NOT ENFORCED", "a > 0", "not enforced"},
		{"nested parens", "CHECK ((a > 0) AND (b < 10))", "(a > 0) AND (b < 10)", ""},
		{"casing folds in suffix", "CHECK (x = 1) Not Enforced", "x = 1", "not enforced"},
		{"missing CHECK keyword falls back to whole def", "(a > 0)", "(a > 0)", ""},
		{"CHECK without opening paren falls back", "CHECK a > 0", "CHECK a > 0", ""},
		{"unmatched parens fall back", "CHECK (a > 0", "CHECK (a > 0", ""},
		{"leading whitespace tolerated", "  CHECK (a > 0)", "a > 0", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			expr, suffix := diff.SplitCheckDef(tt.def)
			assert.Equal(t, tt.wantExpr, expr)
			assert.Equal(t, tt.wantSuffix, suffix)
		})
	}
}

func TestCanonicalExpr(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		wantS string
		wantB bool
	}{
		{"int literal", "42", "42", true},
		{"current_timestamp casing", "CURRENT_TIMESTAMP", "current_timestamp()", true},
		{"unparseable returns input + false", "garbage )(", "garbage )(", false},
		{"multi-expr select returns input + false", "1, 2", "1, 2", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := diff.CanonicalExpr(tt.in)
			assert.Equal(t, tt.wantS, got)
			assert.Equal(t, tt.wantB, ok)
		})
	}
}
