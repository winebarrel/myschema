package catalog_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/winebarrel/myschema/catalog"
)

func sptr(s string) *string { return &s }

func TestNormalizeRefOpt(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty stays empty", "", ""},
		{"RESTRICT collapses to empty", "RESTRICT", ""},
		{"NO ACTION collapses to empty", "NO ACTION", ""},
		{"lowercase no action also collapses", "no action", ""},
		{"CASCADE preserved", "CASCADE", "CASCADE"},
		{"lowercase cascade upper-cased", "cascade", "CASCADE"},
		{"SET NULL preserved", "SET NULL", "SET NULL"},
		{"SET DEFAULT preserved", "SET DEFAULT", "SET DEFAULT"},
		{"unknown action falls through to empty", "WHATEVER", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, catalog.NormalizeRefOpt(tt.in))
		})
	}
}

func TestNormalizeMatch(t *testing.T) {
	assert.Equal(t, "", catalog.NormalizeMatch(""), "empty stays empty")
	assert.Equal(t, "", catalog.NormalizeMatch("NONE"), "NONE collapses to empty")
	assert.Equal(t, "", catalog.NormalizeMatch("none"), "lowercase none also collapses")
	assert.Equal(t, "FULL", catalog.NormalizeMatch("FULL"), "FULL preserved")
	assert.Equal(t, "PARTIAL", catalog.NormalizeMatch("partial"), "lowercase upper-cased")
	assert.Equal(t, "SIMPLE", catalog.NormalizeMatch("Simple"), "mixed case upper-cased")
}

func TestColumnTypeAllowsEmptyStringDefault(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"varchar accepts", "varchar(64)", true},
		{"char accepts", "char(8)", true},
		{"varbinary accepts", "varbinary(32)", true},
		{"enum accepts", "enum('a','b')", true},
		{"set accepts", "set('a','b')", true},
		{"binary refused (uses 0x sentinel instead)", "binary(16)", false},
		{"int refused", "int", false},
		{"text refused", "text", false},
		{"json refused", "json", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, catalog.ColumnTypeAllowsEmptyStringDefault(tt.in))
		})
	}
}

func TestNullIfMatchesTableDefault(t *testing.T) {
	t.Run("col nil passes through", func(t *testing.T) {
		assert.Nil(t, catalog.NullIfMatchesTableDefault(nil, sptr("utf8mb4")))
	})
	t.Run("table default nil passes col through", func(t *testing.T) {
		got := catalog.NullIfMatchesTableDefault(sptr("utf8mb4"), nil)
		assert.Equal(t, "utf8mb4", *got)
	})
	t.Run("matching collapses to nil", func(t *testing.T) {
		assert.Nil(t, catalog.NullIfMatchesTableDefault(sptr("utf8mb4"), sptr("utf8mb4")))
	})
	t.Run("differing passes col through", func(t *testing.T) {
		got := catalog.NullIfMatchesTableDefault(sptr("latin1"), sptr("utf8mb4"))
		assert.Equal(t, "latin1", *got)
	})
}

func TestNormalizeColumnDefault(t *testing.T) {
	tests := []struct {
		name     string
		typeName string
		def      string
		want     string
	}{
		{"empty default on varchar wraps to ''", "varchar(64)", "", "''"},
		{"empty default on int passes through", "int", "", ""},
		{"BINARY 0x sentinel rewrites to ''", "binary(16)", "0x", "''"},
		{"BINARY non-sentinel passes through", "binary(16)", "0x41", "0x41"},
		{"int literal passes through unchanged", "int", "42", "42"},
		{"current_timestamp() passes through unchanged", "timestamp", "current_timestamp()", "current_timestamp()"},
		{"bareword on varchar gets quoted (parser sees ColName)", "varchar(64)", "hello", "'hello'"},
		{"single-quoted string passes through", "varchar(64)", "'hello'", "'hello'"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, catalog.NormalizeColumnDefault(tt.typeName, tt.def))
		})
	}
}
