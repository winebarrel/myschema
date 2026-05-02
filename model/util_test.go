package model_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/winebarrel/myschema/model"
)

func TestIdent(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want string
	}{
		{"single safe identifier", []string{"users"}, "users"},
		{"db + table", []string{"shop", "orders"}, "shop.orders"},
		{"empty parts skipped", []string{"", "users"}, "users"},
		{"reserved word backtick-quoted", []string{"select"}, "`select`"},
		{"reserved word case-insensitive", []string{"TABLE"}, "`TABLE`"},
		{"unsafe characters quoted", []string{"weird-name"}, "`weird-name`"},
		{"backtick in name escaped", []string{"name`with"}, "`name``with`"},
		{"empty input → empty output", []string{}, ""},
		{"all-empty input → empty output", []string{"", ""}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, model.Ident(tt.in...))
		})
	}
}

func TestQuoteLiteral(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"hello", "'hello'"},
		{"it's", "'it''s'"},
		{`back\slash`, `'back\\slash'`},
		{"", "''"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			assert.Equal(t, tt.want, model.QuoteLiteral(tt.in))
		})
	}
}
