package parser_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

func TestEffectiveCharsetForCollation(t *testing.T) {
	t.Run("explicit charset wins", func(t *testing.T) {
		got := parser.EffectiveCharsetForCollation(new("utf8mb4"), new("utf8mb4_bin"))
		require.NotNil(t, got)
		assert.Equal(t, "utf8mb4", *got)
	})
	t.Run("nil collation passes charset through unchanged (incl. nil)", func(t *testing.T) {
		assert.Nil(t, parser.EffectiveCharsetForCollation(nil, nil))
	})
	t.Run("derives charset from collation when charset is nil", func(t *testing.T) {
		got := parser.EffectiveCharsetForCollation(nil, new("utf8mb4_0900_ai_ci"))
		require.NotNil(t, got)
		assert.Equal(t, "utf8mb4", *got)
	})
	t.Run("empty collation string can't be derived from", func(t *testing.T) {
		assert.Nil(t, parser.EffectiveCharsetForCollation(nil, new("")))
	})
}

func TestRejectMisplacedConvertCharset(t *testing.T) {
	assert.NoError(t, parser.RejectMisplacedConvertCharset(false, "CREATE VIEW"))
	err := parser.RejectMisplacedConvertCharset(true, "CREATE VIEW")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "convert-charset")
	assert.Contains(t, err.Error(), "CREATE VIEW")
}

func TestRejectMisplacedRenameDirectives(t *testing.T) {
	t.Run("nothing set is fine", func(t *testing.T) {
		assert.NoError(t, parser.RejectMisplacedRenameDirectives("", &parser.InlineRenames{}, "CREATE VIEW"))
	})
	t.Run("statement-level rename rejected", func(t *testing.T) {
		err := parser.RejectMisplacedRenameDirectives("old_t", &parser.InlineRenames{}, "CREATE VIEW")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "renamed-from old_t")
		assert.Contains(t, err.Error(), "CREATE VIEW")
	})
	t.Run("inline column rename rejected", func(t *testing.T) {
		err := parser.RejectMisplacedRenameDirectives("", &parser.InlineRenames{Columns: map[string]string{"new": "old"}}, "ALTER TABLE")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "ALTER TABLE")
	})
	t.Run("inline index rename rejected", func(t *testing.T) {
		err := parser.RejectMisplacedRenameDirectives("", &parser.InlineRenames{Indexes: map[string]string{"new": "old"}}, "CREATE INDEX")
		require.Error(t, err)
	})
	t.Run("inline constraint rename rejected", func(t *testing.T) {
		err := parser.RejectMisplacedRenameDirectives("", &parser.InlineRenames{Constraints: map[string]string{"new": "old"}}, "ALTER TABLE")
		require.Error(t, err)
	})
	t.Run("inline foreign-key rename rejected", func(t *testing.T) {
		err := parser.RejectMisplacedRenameDirectives("", &parser.InlineRenames{ForeignKeys: map[string]string{"new": "old"}}, "ALTER TABLE")
		require.Error(t, err)
	})
}

func TestValidateExecuteCheckSQL(t *testing.T) {
	tests := []struct {
		name      string
		sql       string
		wantError string // empty = no error
	}{
		{"plain SELECT is fine", "SELECT 1", ""},
		{"SELECT EXISTS is fine", "SELECT EXISTS (SELECT 1 FROM t)", ""},
		{"UNION is fine", "SELECT 1 UNION SELECT 2", ""},
		{"unparseable surfaces", "garbage", "does not parse"},
		{"empty string fails the count check", "", "exactly one statement"},
		{"two statements rejected", "SELECT 1; SELECT 2", "exactly one statement"},
		{"INSERT rejected", "INSERT INTO t VALUES (1)", "must be a SELECT"},
		{"DDL rejected", "CREATE TABLE x (id INT)", "must be a SELECT"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := parser.ValidateExecuteCheckSQL(tt.sql)
			if tt.wantError == "" {
				assert.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantError)
			}
		})
	}
}

func TestReadSQLFile(t *testing.T) {
	t.Run("reads a real file", func(t *testing.T) {
		f := filepath.Join(t.TempDir(), "x.sql")
		require.NoError(t, os.WriteFile(f, []byte("CREATE TABLE t (id INT);"), 0o644))
		got, err := parser.ReadSQLFile(f)
		require.NoError(t, err)
		assert.Equal(t, "CREATE TABLE t (id INT);", got)
	})
	t.Run("missing file errors", func(t *testing.T) {
		_, err := parser.ReadSQLFile(filepath.Join(t.TempDir(), "no_such_file.sql"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to read SQL file")
	})
}

func TestParseSQLFiles(t *testing.T) {
	t.Run("concatenates multiple files", func(t *testing.T) {
		dir := t.TempDir()
		f1 := filepath.Join(dir, "a.sql")
		f2 := filepath.Join(dir, "b.sql")
		require.NoError(t, os.WriteFile(f1, []byte("CREATE TABLE t1 (id INT);"), 0o644))
		require.NoError(t, os.WriteFile(f2, []byte("CREATE TABLE t2 (id INT);"), 0o644))

		r, err := parser.ParseSQLFiles([]string{f1, f2}, "app")
		require.NoError(t, err)
		_, ok1 := r.Tables.GetOk("app.t1")
		_, ok2 := r.Tables.GetOk("app.t2")
		assert.True(t, ok1)
		assert.True(t, ok2)
	})
	t.Run("missing file surfaces ReadSQLFile's error", func(t *testing.T) {
		_, err := parser.ParseSQLFiles([]string{filepath.Join(t.TempDir(), "missing.sql")}, "app")
		require.Error(t, err)
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
