package diff_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/winebarrel/myschema/diff"
	"github.com/winebarrel/myschema/parser"
)

// allowAll / allowList helpers cut down the noise in test bodies.
var allowAll = diff.AllowAll{}

func allowList(kinds ...string) *diff.AllowList {
	m := map[string]bool{}
	for _, k := range kinds {
		m[k] = true
	}
	return &diff.AllowList{Kinds: m}
}

// parsePair is the most common pattern: parse a "current" SQL and a
// "desired" SQL into two table maps and return them ready for DiffTables.
func parsePair(t *testing.T, current, desired string) (cur, des *parser.ParseResult) {
	t.Helper()
	c, err := parser.ParseSQL(current, "app")
	require.NoError(t, err)
	d, err := parser.ParseSQL(desired, "app")
	require.NoError(t, err)
	return c, d
}

func TestDiffEmptyToOneTable(t *testing.T) {
	cur, des := parsePair(t, "", `
CREATE TABLE users (
    id BIGINT NOT NULL AUTO_INCREMENT,
    email VARCHAR(255) NOT NULL,
    PRIMARY KEY (id),
    UNIQUE KEY users_email_key (email)
) ENGINE=InnoDB;
`)
	r, err := diff.DiffTables(cur.Tables, des.Tables, allowAll)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(r.Stmts), 1)
	assert.Contains(t, r.Stmts[0], "CREATE TABLE app.users")

	hasIdx := false
	for _, s := range r.Stmts {
		if strings.Contains(s, "CREATE UNIQUE INDEX users_email_key ON app.users") {
			hasIdx = true
		}
	}
	assert.True(t, hasIdx, "expected separate CREATE UNIQUE INDEX statement")
}

func TestDiffAddColumn(t *testing.T) {
	cur, des := parsePair(t,
		`CREATE TABLE users (id BIGINT NOT NULL, PRIMARY KEY (id));`,
		`CREATE TABLE users (id BIGINT NOT NULL, name VARCHAR(64) NOT NULL, PRIMARY KEY (id));`,
	)
	r, err := diff.DiffTables(cur.Tables, des.Tables, allowAll)
	require.NoError(t, err)
	require.Len(t, r.Stmts, 1)
	assert.Equal(t, "ALTER TABLE app.users ADD COLUMN name varchar(64) NOT NULL;", r.Stmts[0])
}

func TestDiffDropColumnSuppressed(t *testing.T) {
	cur, des := parsePair(t,
		`CREATE TABLE users (id BIGINT NOT NULL, legacy VARCHAR(64), PRIMARY KEY (id));`,
		`CREATE TABLE users (id BIGINT NOT NULL, PRIMARY KEY (id));`,
	)
	r, err := diff.DiffTables(cur.Tables, des.Tables, allowList()) // no allow-drop
	require.NoError(t, err)
	assert.Empty(t, r.Stmts)
	require.Len(t, r.DisallowedDropStmts, 1)
	assert.Contains(t, r.DisallowedDropStmts[0], "-- skipped: ALTER TABLE app.users DROP COLUMN legacy")
}

func TestDiffDropColumnAllowed(t *testing.T) {
	cur, des := parsePair(t,
		`CREATE TABLE users (id BIGINT NOT NULL, legacy VARCHAR(64), PRIMARY KEY (id));`,
		`CREATE TABLE users (id BIGINT NOT NULL, PRIMARY KEY (id));`,
	)
	r, err := diff.DiffTables(cur.Tables, des.Tables, allowList("column"))
	require.NoError(t, err)
	require.Len(t, r.Stmts, 1)
	assert.Equal(t, "ALTER TABLE app.users DROP COLUMN legacy;", r.Stmts[0])
}

func TestDiffModifyColumnType(t *testing.T) {
	cur, des := parsePair(t,
		`CREATE TABLE users (id BIGINT NOT NULL, name VARCHAR(64), PRIMARY KEY (id));`,
		`CREATE TABLE users (id BIGINT NOT NULL, name VARCHAR(255), PRIMARY KEY (id));`,
	)
	r, err := diff.DiffTables(cur.Tables, des.Tables, allowAll)
	require.NoError(t, err)
	require.Len(t, r.Stmts, 1)
	assert.Equal(t, "ALTER TABLE app.users MODIFY COLUMN name varchar(255);", r.Stmts[0])
}

func TestDiffModifyColumnDefault(t *testing.T) {
	cur, des := parsePair(t,
		`CREATE TABLE users (id BIGINT NOT NULL, status VARCHAR(16) NOT NULL DEFAULT 'a', PRIMARY KEY (id));`,
		`CREATE TABLE users (id BIGINT NOT NULL, status VARCHAR(16) NOT NULL DEFAULT 'b', PRIMARY KEY (id));`,
	)
	r, err := diff.DiffTables(cur.Tables, des.Tables, allowAll)
	require.NoError(t, err)
	require.Len(t, r.Stmts, 1)
	assert.Contains(t, r.Stmts[0], "DEFAULT 'b'")
}

func TestDiffDropTableSuppressed(t *testing.T) {
	cur, des := parsePair(t, `CREATE TABLE legacy (id INT NOT NULL, PRIMARY KEY (id));`, "")
	r, err := diff.DiffTables(cur.Tables, des.Tables, allowList())
	require.NoError(t, err)
	assert.Empty(t, r.Stmts)
	assert.Empty(t, r.DropStmts)
	require.Len(t, r.DisallowedDropStmts, 1)
	assert.Contains(t, r.DisallowedDropStmts[0], "-- skipped: DROP TABLE app.legacy")
}

func TestDiffDropTableAllowed(t *testing.T) {
	cur, des := parsePair(t, `CREATE TABLE legacy (id INT NOT NULL, PRIMARY KEY (id));`, "")
	r, err := diff.DiffTables(cur.Tables, des.Tables, allowList("table"))
	require.NoError(t, err)
	require.Len(t, r.DropStmts, 1)
	assert.Equal(t, "DROP TABLE app.legacy;", r.DropStmts[0])
}

func TestDiffAddCheckConstraint(t *testing.T) {
	cur, des := parsePair(t,
		`CREATE TABLE products (id INT NOT NULL, price INT NOT NULL, PRIMARY KEY (id));`,
		`CREATE TABLE products (id INT NOT NULL, price INT NOT NULL, PRIMARY KEY (id), CONSTRAINT chk CHECK (price > 0));`,
	)
	r, err := diff.DiffTables(cur.Tables, des.Tables, allowAll)
	require.NoError(t, err)
	require.Len(t, r.Stmts, 1)
	assert.Contains(t, r.Stmts[0], "ADD CONSTRAINT chk CHECK")
}

func TestDiffDropCheckConstraintAllowed(t *testing.T) {
	cur, des := parsePair(t,
		`CREATE TABLE products (id INT NOT NULL, price INT NOT NULL, PRIMARY KEY (id), CONSTRAINT chk CHECK (price > 0));`,
		`CREATE TABLE products (id INT NOT NULL, price INT NOT NULL, PRIMARY KEY (id));`,
	)
	r, err := diff.DiffTables(cur.Tables, des.Tables, allowList("constraint"))
	require.NoError(t, err)
	require.Len(t, r.Stmts, 1)
	assert.Equal(t, "ALTER TABLE app.products DROP CHECK chk;", r.Stmts[0])
}

func TestDiffDropCheckConstraintSuppressed(t *testing.T) {
	cur, des := parsePair(t,
		`CREATE TABLE products (id INT NOT NULL, price INT NOT NULL, PRIMARY KEY (id), CONSTRAINT chk CHECK (price > 0));`,
		`CREATE TABLE products (id INT NOT NULL, price INT NOT NULL, PRIMARY KEY (id));`,
	)
	r, err := diff.DiffTables(cur.Tables, des.Tables, allowList())
	require.NoError(t, err)
	assert.Empty(t, r.Stmts)
	require.Len(t, r.DisallowedDropStmts, 1)
	assert.Contains(t, r.DisallowedDropStmts[0], "-- skipped: ALTER TABLE app.products DROP CHECK chk")
}

func TestDiffAddIndex(t *testing.T) {
	cur, des := parsePair(t,
		`CREATE TABLE users (id BIGINT NOT NULL, email VARCHAR(255), PRIMARY KEY (id));`,
		`CREATE TABLE users (id BIGINT NOT NULL, email VARCHAR(255), PRIMARY KEY (id), KEY idx_email (email));`,
	)
	r, err := diff.DiffTables(cur.Tables, des.Tables, allowAll)
	require.NoError(t, err)
	require.Len(t, r.Stmts, 1)
	assert.Contains(t, r.Stmts[0], "CREATE INDEX idx_email ON app.users")
}

func TestDiffDropIndexSuppressed(t *testing.T) {
	cur, des := parsePair(t,
		`CREATE TABLE users (id BIGINT NOT NULL, email VARCHAR(255), PRIMARY KEY (id), KEY idx_email (email));`,
		`CREATE TABLE users (id BIGINT NOT NULL, email VARCHAR(255), PRIMARY KEY (id));`,
	)
	r, err := diff.DiffTables(cur.Tables, des.Tables, allowList())
	require.NoError(t, err)
	assert.Empty(t, r.Stmts)
	require.Len(t, r.DisallowedDropStmts, 1)
	assert.Contains(t, r.DisallowedDropStmts[0], "-- skipped: ALTER TABLE app.users DROP INDEX idx_email")
}

func TestDiffAddForeignKey(t *testing.T) {
	cur, des := parsePair(t,
		`CREATE TABLE users (id BIGINT NOT NULL, PRIMARY KEY (id));
CREATE TABLE posts (id BIGINT NOT NULL, user_id BIGINT NOT NULL, PRIMARY KEY (id));`,
		`CREATE TABLE users (id BIGINT NOT NULL, PRIMARY KEY (id));
CREATE TABLE posts (id BIGINT NOT NULL, user_id BIGINT NOT NULL, PRIMARY KEY (id),
  CONSTRAINT fk FOREIGN KEY (user_id) REFERENCES users(id));`,
	)
	r, err := diff.DiffTables(cur.Tables, des.Tables, allowAll)
	require.NoError(t, err)
	require.Len(t, r.FKAddStmts, 1)
	assert.Contains(t, r.FKAddStmts[0], "ADD CONSTRAINT fk FOREIGN KEY (user_id) REFERENCES app.users(id)")
}

func TestDiffDropForeignKeySuppressed(t *testing.T) {
	cur, des := parsePair(t,
		`CREATE TABLE users (id BIGINT NOT NULL, PRIMARY KEY (id));
CREATE TABLE posts (id BIGINT NOT NULL, user_id BIGINT NOT NULL, PRIMARY KEY (id),
  CONSTRAINT fk FOREIGN KEY (user_id) REFERENCES users(id));`,
		`CREATE TABLE users (id BIGINT NOT NULL, PRIMARY KEY (id));
CREATE TABLE posts (id BIGINT NOT NULL, user_id BIGINT NOT NULL, PRIMARY KEY (id));`,
	)
	r, err := diff.DiffTables(cur.Tables, des.Tables, allowList())
	require.NoError(t, err)
	assert.Empty(t, r.FKDropStmts)
	require.Len(t, r.DisallowedDropStmts, 1)
	assert.Contains(t, r.DisallowedDropStmts[0], "DROP FOREIGN KEY fk")
}

// TestDiffDropTableWithFK verifies DiffTables emits FK drops on a
// being-dropped table BEFORE the table drop, and respects the table-drop
// policy: when --allow-drop=table is unset, both the FK drop and the
// table drop are suppressed together.
func TestDiffDropTableWithFK(t *testing.T) {
	current, _ := parsePair(t,
		`CREATE TABLE users (id BIGINT NOT NULL, PRIMARY KEY (id));
CREATE TABLE posts (id BIGINT NOT NULL, user_id BIGINT NOT NULL, PRIMARY KEY (id),
  CONSTRAINT fk FOREIGN KEY (user_id) REFERENCES users(id));`,
		"")
	desired, _ := parsePair(t, "", "")

	t.Run("allow_drop=table emits FK drop then table drop", func(t *testing.T) {
		r, err := diff.DiffTables(current.Tables, desired.Tables, allowList("table"))
		require.NoError(t, err)
		require.Len(t, r.FKDropStmts, 1)
		assert.Contains(t, r.FKDropStmts[0], "DROP FOREIGN KEY fk")
		require.Len(t, r.DropStmts, 2) // posts + users
	})

	t.Run("no allow_drop suppresses both", func(t *testing.T) {
		r, err := diff.DiffTables(current.Tables, desired.Tables, allowList())
		require.NoError(t, err)
		assert.Empty(t, r.FKDropStmts)
		assert.Empty(t, r.DropStmts)
		// Disallowed drops include FK comment + 2 table comments
		assert.NotEmpty(t, r.DisallowedDropStmts)
	})
}

// TestDropPolicyWildcard pins the "all" wildcard semantics: nothing
// goes to DisallowedDropStmts. The dependent-index-suppression case
// (column drop alone, with the index dropped automatically by MySQL)
// has its own dedicated test below.
func TestDropPolicyWildcard(t *testing.T) {
	cur, des := parsePair(t,
		`CREATE TABLE legacy_table (id INT NOT NULL, PRIMARY KEY (id));`,
		``,
	)
	r, err := diff.DiffTables(cur.Tables, des.Tables, allowList("all"))
	require.NoError(t, err)
	assert.Empty(t, r.DisallowedDropStmts)
	require.Len(t, r.DropStmts, 1)
	assert.Contains(t, r.DropStmts[0], "DROP TABLE app.legacy_table")
}

// TestDiffDropColumnSuppressesDependentIndex: when a column with a
// single-column index is dropped, the explicit DROP INDEX is suppressed
// because MySQL removes the index automatically alongside the column.
// See allPartsDropped in diff/tables.go.
func TestDiffDropColumnSuppressesDependentIndex(t *testing.T) {
	cur, des := parsePair(t,
		`CREATE TABLE users (id BIGINT NOT NULL, legacy VARCHAR(64), PRIMARY KEY (id), KEY i (legacy));`,
		`CREATE TABLE users (id BIGINT NOT NULL, PRIMARY KEY (id));`,
	)
	r, err := diff.DiffTables(cur.Tables, des.Tables, allowList("all"))
	require.NoError(t, err)
	body := strings.Join(r.Stmts, "\n")
	assert.Contains(t, body, "DROP COLUMN legacy")
	assert.NotContains(t, body, "DROP INDEX i", "explicit DROP INDEX should be suppressed")
}

// TestDiffDropAllColumnsSuppressesCombinedIndex: same for a multi-column
// index where every part is being dropped.
func TestDiffDropAllColumnsSuppressesCombinedIndex(t *testing.T) {
	cur, des := parsePair(t,
		`CREATE TABLE users (id BIGINT NOT NULL, a VARCHAR(64), b VARCHAR(64), PRIMARY KEY (id), KEY i (a, b));`,
		`CREATE TABLE users (id BIGINT NOT NULL, PRIMARY KEY (id));`,
	)
	r, err := diff.DiffTables(cur.Tables, des.Tables, allowList("all"))
	require.NoError(t, err)
	body := strings.Join(r.Stmts, "\n")
	assert.NotContains(t, body, "DROP INDEX i", "combined-index DROP should be suppressed when all its columns are dropped")
}

// TestDiffReplaceIndexSuppressesDependentDrop: the suppression must
// also catch the DROP+CREATE replacement path. If an index keeps the
// same name but moves to different columns, AND the original columns
// are being dropped, MySQL auto-removes the old index during DROP
// COLUMN — the explicit DROP INDEX would still error 1091. The
// CREATE INDEX for the new shape must still fire.
func TestDiffReplaceIndexSuppressesDependentDrop(t *testing.T) {
	cur, des := parsePair(t,
		`CREATE TABLE t (id INT NOT NULL, a VARCHAR(64), b VARCHAR(64), PRIMARY KEY (id), KEY i (a));`,
		`CREATE TABLE t (id INT NOT NULL, b VARCHAR(64), PRIMARY KEY (id), KEY i (b));`,
	)
	r, err := diff.DiffTables(cur.Tables, des.Tables, allowList("all"))
	require.NoError(t, err)
	body := strings.Join(r.Stmts, "\n")
	assert.Contains(t, body, "DROP COLUMN a")
	assert.NotContains(t, body, "DROP INDEX i", "DROP INDEX must be suppressed even on the replace path when all current parts are dropped columns")
	assert.Contains(t, body, "CREATE INDEX i ON app.t (b)", "the new index shape still gets created")
}

// TestDiffDropIndexNotSuppressedWhenColumnDropDisallowed: the
// suppression must respect drop-policy gating. If the user allows
// `index` drops but NOT `column` drops, the column won't actually go
// away — so MySQL won't remove the index either, and we still need
// the explicit DROP INDEX. (Earlier implementation had a bug here.)
func TestDiffDropIndexNotSuppressedWhenColumnDropDisallowed(t *testing.T) {
	cur, des := parsePair(t,
		`CREATE TABLE users (id BIGINT NOT NULL, legacy VARCHAR(64), PRIMARY KEY (id), KEY i (legacy));`,
		`CREATE TABLE users (id BIGINT NOT NULL, PRIMARY KEY (id));`,
	)
	r, err := diff.DiffTables(cur.Tables, des.Tables, allowList("index"))
	require.NoError(t, err)
	body := strings.Join(r.Stmts, "\n")
	assert.Contains(t, body, "DROP INDEX i",
		"DROP INDEX must be emitted because the column drop is disallowed and won't actually run")
	disallowed := strings.Join(r.DisallowedDropStmts, "\n")
	assert.Contains(t, disallowed, "DROP COLUMN legacy",
		"column drop should be in DisallowedDropStmts (not allowed)")
}

// TestDiffDropPartialColumnKeepsIndex: when only some of an index's
// parts are being dropped, the explicit DROP INDEX must still be
// emitted — MySQL won't auto-remove a multi-column index when only
// one of its columns goes away (it would error on the column drop).
func TestDiffDropPartialColumnKeepsIndex(t *testing.T) {
	cur, des := parsePair(t,
		`CREATE TABLE users (id BIGINT NOT NULL, a VARCHAR(64), b VARCHAR(64), PRIMARY KEY (id), KEY i (a, b));`,
		`CREATE TABLE users (id BIGINT NOT NULL, b VARCHAR(64), PRIMARY KEY (id), KEY i (b));`,
	)
	r, err := diff.DiffTables(cur.Tables, des.Tables, allowList("all"))
	require.NoError(t, err)
	body := strings.Join(r.Stmts, "\n")
	assert.Contains(t, body, "DROP COLUMN a")
	assert.Contains(t, body, "DROP INDEX i", "explicit DROP INDEX needed when index would survive")
}

// TestDropPolicyNilFallsBackToAllowAll: passing nil to DiffTables should
// behave like AllowAll{}, not like an empty AllowList.
func TestDropPolicyNilFallsBackToAllowAll(t *testing.T) {
	cur, des := parsePair(t,
		`CREATE TABLE legacy (id INT NOT NULL, PRIMARY KEY (id));`,
		"",
	)
	r, err := diff.DiffTables(cur.Tables, des.Tables, nil)
	require.NoError(t, err)
	require.Len(t, r.DropStmts, 1, "nil dc should permit drops")
}

// TestDiffNoChangesIsEmpty: identical current / desired yields no
// statements, no disallowed comments. The trivial sanity case.
func TestDiffNoChanges(t *testing.T) {
	sql := `CREATE TABLE users (id BIGINT NOT NULL, PRIMARY KEY (id));`
	cur, des := parsePair(t, sql, sql)
	r, err := diff.DiffTables(cur.Tables, des.Tables, allowAll)
	require.NoError(t, err)
	assert.Empty(t, r.Stmts)
	assert.Empty(t, r.DropStmts)
	assert.Empty(t, r.FKDropStmts)
	assert.Empty(t, r.FKAddStmts)
	assert.Empty(t, r.DisallowedDropStmts)
}
