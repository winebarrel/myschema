package myschema_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/winebarrel/myschema"
)

func TestSkipIdentifier(t *testing.T) {
	tests := []struct {
		name     string
		s        string
		startPos int
		wantPos  int
	}{
		{"pos at end returns pos unchanged", "abc", 3, 3},
		{"plain identifier", "users next", 0, 5},
		{"identifier with digits and underscore", "_a$1 next", 0, 4},
		{"backtick-quoted simple", "`users` next", 0, 7},
		{"backtick-quoted with escaped `` inside", "`a``b` rest", 0, 6},
		{"backtick-quoted unterminated runs to end", "`abc", 0, 4},
		{"non-identifier char halts", "+x", 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.wantPos, myschema.SkipIdentifier(tt.s, tt.startPos))
		})
	}
}
