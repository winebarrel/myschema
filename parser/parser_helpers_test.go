package parser_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"vitess.io/vitess/go/vt/sqlparser"

	"github.com/winebarrel/myschema/parser"
)

func TestAutoCheckName(t *testing.T) {
	// CHECK constraints emitted without an explicit name follow MySQL's
	// own auto-naming convention (`<table>_chk_<n+1>`), so each
	// successive unnamed CHECK on the same table gets a unique name.
	assert.Equal(t, "products_chk_1", parser.AutoCheckName("products", 0))
	assert.Equal(t, "products_chk_2", parser.AutoCheckName("products", 1))
	assert.Equal(t, "t_chk_10", parser.AutoCheckName("t", 9))
}

func TestAutoFKName(t *testing.T) {
	assert.Equal(t, "orders_ibfk_user_id", parser.AutoFKName("orders", "user_id"))
	assert.Equal(t, "t_ibfk_x", parser.AutoFKName("t", "x"))
}

func TestDBName(t *testing.T) {
	assert.Equal(t, "explicit", parser.DBName("explicit", "fallback"), "explicit schema wins")
	assert.Equal(t, "fallback", parser.DBName("", "fallback"), "empty schema falls back to default")
	assert.Equal(t, "", parser.DBName("", ""), "both empty stays empty")
}

func TestReferenceActionString(t *testing.T) {
	tests := []struct {
		name string
		in   sqlparser.ReferenceAction
		want string
	}{
		{"CASCADE", sqlparser.Cascade, "CASCADE"},
		{"SET NULL", sqlparser.SetNull, "SET NULL"},
		{"SET DEFAULT", sqlparser.SetDefault, "SET DEFAULT"},
		{"NO ACTION collapses to empty (catalog convention)", sqlparser.NoAction, ""},
		{"RESTRICT collapses to empty (catalog convention)", sqlparser.Restrict, ""},
		{"default-zero collapses to empty", sqlparser.DefaultAction, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, parser.ReferenceActionString(tt.in))
		})
	}
}

func TestNormalizeDefaultExpr(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"unrelated literal passes through", "42", "42"},
		{"trims whitespace", "  42  ", "42"},
		{"current_timestamp() → CURRENT_TIMESTAMP", "current_timestamp()", "CURRENT_TIMESTAMP"},
		{"upper-case input still folds", "CURRENT_TIMESTAMP()", "CURRENT_TIMESTAMP"},
		{"current_timestamp(6) preserves precision", "current_timestamp(6)", "CURRENT_TIMESTAMP(6)"},
		{"now() folds to NOW", "now()", "NOW"},
		{"localtime() folds to LOCALTIME", "localtime()", "LOCALTIME"},
		{"utc_timestamp(3) preserves precision", "utc_timestamp(3)", "UTC_TIMESTAMP(3)"},
		{"non-magic function passes through trimmed", "  myfn()  ", "myfn()"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, parser.NormalizeDefaultExpr(tt.in))
		})
	}
}

func TestRestoreSelectLower(t *testing.T) {
	t.Run("nil node returns empty", func(t *testing.T) {
		got, err := parser.RestoreSelectLower(nil)
		assert.NoError(t, err)
		assert.Equal(t, "", got)
	})
}

func TestParseCheckConstraintAutoName(t *testing.T) {
	// Pin the integration path: parser routes an unnamed CHECK through
	// autoCheckName, producing the MySQL-compatible "<table>_chk_<n+1>"
	// name.
	r, err := parser.ParseSQL(`
CREATE TABLE products (
    price INT NOT NULL,
    qty   INT NOT NULL,
    CHECK (price > 0),
    CHECK (qty >= 0)
);
`, "app")
	if err != nil {
		t.Fatal(err)
	}
	tbl, ok := r.Tables.GetOk("app.products")
	if !ok {
		t.Fatal("table app.products not parsed")
	}
	_, ok = tbl.Constraints.GetOk("products_chk_1")
	assert.True(t, ok, "first unnamed CHECK should be auto-named products_chk_1")
	_, ok = tbl.Constraints.GetOk("products_chk_2")
	assert.True(t, ok, "second unnamed CHECK should be auto-named products_chk_2")
}
