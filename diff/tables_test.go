package diff_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/winebarrel/myschema/diff"
	"github.com/winebarrel/myschema/parser"
	"github.com/winebarrel/orderedmap"
)

func parseOne(t *testing.T, sql string) *orderedmap.Map[string, *struct{}] {
	t.Helper()
	_, err := parser.ParseSQL(sql, "app")
	require.NoError(t, err)
	return nil
}

func TestDiffEmptyToOneTable(t *testing.T) {
	desired, err := parser.ParseSQL(`
CREATE TABLE users (
    id BIGINT NOT NULL AUTO_INCREMENT,
    email VARCHAR(255) NOT NULL,
    PRIMARY KEY (id),
    UNIQUE KEY users_email_key (email)
) ENGINE=InnoDB;
`, "app")
	require.NoError(t, err)

	current, err := parser.ParseSQL("", "app")
	require.NoError(t, err)

	r, err := diff.DiffTables(current.Tables, desired.Tables, diff.AllowAll{})
	require.NoError(t, err)

	require.GreaterOrEqual(t, len(r.Stmts), 1, "should emit at least the CREATE TABLE")
	assert.Contains(t, r.Stmts[0], "CREATE TABLE app.users")
	// secondary index should be a separate statement
	hasIdx := false
	for _, s := range r.Stmts {
		if strings.Contains(s, "CREATE UNIQUE INDEX users_email_key ON app.users") {
			hasIdx = true
		}
	}
	assert.True(t, hasIdx, "expected separate CREATE UNIQUE INDEX statement")
}

func TestDiffAddColumn(t *testing.T) {
	current, err := parser.ParseSQL(`
CREATE TABLE users (
    id BIGINT NOT NULL,
    PRIMARY KEY (id)
);
`, "app")
	require.NoError(t, err)
	desired, err := parser.ParseSQL(`
CREATE TABLE users (
    id BIGINT NOT NULL,
    name VARCHAR(64) NOT NULL,
    PRIMARY KEY (id)
);
`, "app")
	require.NoError(t, err)

	r, err := diff.DiffTables(current.Tables, desired.Tables, diff.AllowAll{})
	require.NoError(t, err)

	require.Len(t, r.Stmts, 1)
	assert.Equal(t,
		"ALTER TABLE app.users ADD COLUMN name varchar(64) NOT NULL;",
		r.Stmts[0])
	_ = parseOne // silence unused-helper warning
}

func TestDiffDropTableSuppressed(t *testing.T) {
	current, err := parser.ParseSQL("CREATE TABLE legacy (id INT NOT NULL, PRIMARY KEY (id));", "app")
	require.NoError(t, err)
	desired, err := parser.ParseSQL("", "app")
	require.NoError(t, err)

	r, err := diff.DiffTables(current.Tables, desired.Tables, &diff.AllowList{Kinds: map[string]bool{}})
	require.NoError(t, err)

	assert.Empty(t, r.Stmts)
	assert.Empty(t, r.DropStmts)
	require.Len(t, r.DisallowedDropStmts, 1)
	assert.Contains(t, r.DisallowedDropStmts[0], "-- skipped: DROP TABLE app.legacy")
}
