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
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, equalExpr(tt.a, tt.b))
		})
	}
}

func TestEqualExprPtr(t *testing.T) {
	s := func(v string) *string { return &v }

	assert.True(t, equalExprPtr(nil, nil), "both nil")
	assert.False(t, equalExprPtr(s("x"), nil), "one nil")
	assert.False(t, equalExprPtr(nil, s("x")), "other nil")
	assert.True(t, equalExprPtr(s("CURRENT_TIMESTAMP"), s("current_timestamp()")), "canonicalised match")
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
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, equalCheckDef(tt.a, tt.b))
		})
	}
}
