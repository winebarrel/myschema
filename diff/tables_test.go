package diff_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/winebarrel/myschema/diff"
	"github.com/winebarrel/myschema/model"
	"github.com/winebarrel/myschema/parser"
	"github.com/winebarrel/orderedmap"
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
	assert.Contains(t, r.Stmts[0], "CREATE TABLE users (")

	hasIdx := false
	for _, s := range r.Stmts {
		if strings.Contains(s, "CREATE UNIQUE INDEX users_email_key ON users (") {
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
	// ADD COLUMN now carries a positional clause (AFTER / FIRST)
	// derived from the desired-side column order — `name` sits after
	// `id` in desired, so MySQL gets told to put it there instead of
	// silently appending to the row tail.
	assert.Equal(t, "ALTER TABLE users ADD COLUMN name varchar(64) NOT NULL AFTER id;", r.Stmts[0])
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
	assert.Contains(t, r.DisallowedDropStmts[0], "-- skipped: ALTER TABLE users DROP COLUMN legacy;")
}

func TestDiffDropColumnAllowed(t *testing.T) {
	cur, des := parsePair(t,
		`CREATE TABLE users (id BIGINT NOT NULL, legacy VARCHAR(64), PRIMARY KEY (id));`,
		`CREATE TABLE users (id BIGINT NOT NULL, PRIMARY KEY (id));`,
	)
	r, err := diff.DiffTables(cur.Tables, des.Tables, allowList("column"))
	require.NoError(t, err)
	require.Len(t, r.Stmts, 1)
	assert.Equal(t, "ALTER TABLE users DROP COLUMN legacy;", r.Stmts[0])
}

func TestDiffModifyColumnType(t *testing.T) {
	cur, des := parsePair(t,
		`CREATE TABLE users (id BIGINT NOT NULL, name VARCHAR(64), PRIMARY KEY (id));`,
		`CREATE TABLE users (id BIGINT NOT NULL, name VARCHAR(255), PRIMARY KEY (id));`,
	)
	r, err := diff.DiffTables(cur.Tables, des.Tables, allowAll)
	require.NoError(t, err)
	require.Len(t, r.Stmts, 1)
	assert.Equal(t, "ALTER TABLE users MODIFY COLUMN name varchar(255);", r.Stmts[0])
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
	assert.Contains(t, r.DisallowedDropStmts[0], "-- skipped: DROP TABLE legacy")
}

func TestDiffDropTableAllowed(t *testing.T) {
	cur, des := parsePair(t, `CREATE TABLE legacy (id INT NOT NULL, PRIMARY KEY (id));`, "")
	r, err := diff.DiffTables(cur.Tables, des.Tables, allowList("table"))
	require.NoError(t, err)
	require.Len(t, r.DropStmts, 1)
	assert.Equal(t, "DROP TABLE legacy;", r.DropStmts[0])
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
	assert.Equal(t, "ALTER TABLE products DROP CHECK chk;", r.Stmts[0])
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
	assert.Contains(t, r.DisallowedDropStmts[0], "-- skipped: ALTER TABLE products DROP CHECK chk;")
}

func TestDiffAddIndex(t *testing.T) {
	cur, des := parsePair(t,
		`CREATE TABLE users (id BIGINT NOT NULL, email VARCHAR(255), PRIMARY KEY (id));`,
		`CREATE TABLE users (id BIGINT NOT NULL, email VARCHAR(255), PRIMARY KEY (id), KEY idx_email (email));`,
	)
	r, err := diff.DiffTables(cur.Tables, des.Tables, allowAll)
	require.NoError(t, err)
	require.Len(t, r.Stmts, 1)
	assert.Contains(t, r.Stmts[0], "CREATE INDEX idx_email ON users (email)")
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
	assert.Contains(t, r.DisallowedDropStmts[0], "-- skipped: ALTER TABLE users DROP INDEX idx_email;")
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
	assert.Contains(t, r.FKAddStmts[0], "ADD CONSTRAINT fk FOREIGN KEY (user_id) REFERENCES users(id)")
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
	assert.Contains(t, r.DisallowedDropStmts[0], "DROP FOREIGN KEY fk;")
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
		assert.Contains(t, r.FKDropStmts[0], "DROP FOREIGN KEY fk;")
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
	assert.Contains(t, r.DropStmts[0], "DROP TABLE legacy_table;")
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
	assert.Contains(t, body, "DROP COLUMN legacy;")
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
	assert.Contains(t, body, "DROP COLUMN a;")
	assert.NotContains(t, body, "DROP INDEX i", "DROP INDEX must be suppressed even on the replace path when all current parts are dropped columns")
	assert.Contains(t, body, "CREATE INDEX i ON t (b)", "the new index shape still gets created")
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
	assert.Contains(t, body, "DROP INDEX i;",
		"DROP INDEX must be emitted because the column drop is disallowed and won't actually run")
	disallowed := strings.Join(r.DisallowedDropStmts, "\n")
	assert.Contains(t, disallowed, "DROP COLUMN legacy;",
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
	assert.Contains(t, body, "DROP COLUMN a;")
	assert.Contains(t, body, "DROP INDEX i;", "explicit DROP INDEX needed when index would survive")
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

func TestPtrEq(t *testing.T) {
	assert.True(t, diff.PtrEq(nil, nil), "both nil")
	assert.False(t, diff.PtrEq(new("x"), nil), "left set, right nil")
	assert.False(t, diff.PtrEq(nil, new("x")), "left nil, right set")
	assert.True(t, diff.PtrEq(new("x"), new("x")), "both set, equal")
	assert.False(t, diff.PtrEq(new("x"), new("y")), "both set, different")
}

func TestLooseEqual(t *testing.T) {
	tests := []struct {
		name string
		a, b string
		want bool
	}{
		{"identical", "(a, b)", "(a, b)", true},
		{"casing", "(A, B)", "(a, b)", true},
		{"spacing", "(a, b)", "(a,b)", true},
		{"backticks", "(`a`, `b`)", "(a, b)", true},
		{"differ", "(a, b)", "(a, c)", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, diff.LooseEqual(tt.a, tt.b))
		})
	}
}

func TestNormalizeIndexType(t *testing.T) {
	assert.Equal(t, "", diff.NormalizeIndexType(""), "empty stays empty")
	assert.Equal(t, "", diff.NormalizeIndexType("BTREE"), "BTREE collapses to empty (InnoDB default)")
	assert.Equal(t, "", diff.NormalizeIndexType("btree"), "lowercase btree also collapses")
	assert.Equal(t, "HASH", diff.NormalizeIndexType("HASH"), "HASH preserved")
	assert.Equal(t, "FULLTEXT", diff.NormalizeIndexType("fulltext"), "uppercased")
	assert.Equal(t, "SPATIAL", diff.NormalizeIndexType("SPATIAL"), "SPATIAL preserved")
}

func TestColumnEqual(t *testing.T) {
	base := func() *model.Column {
		return &model.Column{
			Name:     "c",
			TypeName: "int",
			NotNull:  true,
		}
	}

	t.Run("identical", func(t *testing.T) {
		assert.True(t, diff.ColumnEqual(base(), base()))
	})
	t.Run("type differs", func(t *testing.T) {
		a, b := base(), base()
		b.TypeName = "bigint"
		assert.False(t, diff.ColumnEqual(a, b))
	})
	t.Run("not-null differs", func(t *testing.T) {
		a, b := base(), base()
		b.NotNull = false
		assert.False(t, diff.ColumnEqual(a, b))
	})
	t.Run("default differs", func(t *testing.T) {
		a, b := base(), base()
		a.Default = new("0")
		b.Default = new("1")
		assert.False(t, diff.ColumnEqual(a, b))
	})
	t.Run("default canonicalised match", func(t *testing.T) {
		a, b := base(), base()
		a.Default = new("CURRENT_TIMESTAMP")
		b.Default = new("current_timestamp()")
		assert.True(t, diff.ColumnEqual(a, b))
	})
	t.Run("on-update differs", func(t *testing.T) {
		a, b := base(), base()
		a.OnUpdate = new("CURRENT_TIMESTAMP")
		assert.False(t, diff.ColumnEqual(a, b))
	})
	t.Run("auto-increment differs", func(t *testing.T) {
		a, b := base(), base()
		b.AutoIncrement = true
		assert.False(t, diff.ColumnEqual(a, b))
	})
	t.Run("invisible differs", func(t *testing.T) {
		a, b := base(), base()
		b.Invisible = true
		assert.False(t, diff.ColumnEqual(a, b))
	})
	t.Run("generated expr differs", func(t *testing.T) {
		a, b := base(), base()
		a.Generated = new("a + 1")
		b.Generated = new("a + 2")
		assert.False(t, diff.ColumnEqual(a, b))
	})
	t.Run("stored differs", func(t *testing.T) {
		a, b := base(), base()
		a.Generated = new("a + 1")
		b.Generated = new("a + 1")
		b.Stored = true
		assert.False(t, diff.ColumnEqual(a, b))
	})
	t.Run("comment differs", func(t *testing.T) {
		a, b := base(), base()
		a.Comment = new("hi")
		assert.False(t, diff.ColumnEqual(a, b))
	})
	t.Run("character set differs", func(t *testing.T) {
		a, b := base(), base()
		a.CharacterSet = new("utf8mb4")
		assert.False(t, diff.ColumnEqual(a, b))
	})
	t.Run("collation differs", func(t *testing.T) {
		a, b := base(), base()
		a.Collation = new("utf8mb4_bin")
		assert.False(t, diff.ColumnEqual(a, b))
	})
}

func TestConstraintEqual(t *testing.T) {
	t.Run("type differs (PK vs CHECK)", func(t *testing.T) {
		a := &model.Constraint{Type: model.PrimaryKeyConstraint, Definition: "(id)"}
		b := &model.Constraint{Type: model.CheckConstraint, Definition: "CHECK (id > 0)"}
		assert.False(t, diff.ConstraintEqual(a, b))
	})
	t.Run("CHECK identical", func(t *testing.T) {
		a := &model.Constraint{Type: model.CheckConstraint, Definition: "CHECK (id > 0)", Enforced: true}
		b := &model.Constraint{Type: model.CheckConstraint, Definition: "CHECK (id > 0)", Enforced: true}
		assert.True(t, diff.ConstraintEqual(a, b))
	})
	t.Run("CHECK enforcement differs", func(t *testing.T) {
		a := &model.Constraint{Type: model.CheckConstraint, Definition: "CHECK (id > 0)", Enforced: true}
		b := &model.Constraint{Type: model.CheckConstraint, Definition: "CHECK (id > 0)", Enforced: false}
		assert.False(t, diff.ConstraintEqual(a, b))
	})
	t.Run("CHECK expression differs", func(t *testing.T) {
		a := &model.Constraint{Type: model.CheckConstraint, Definition: "CHECK (id > 0)", Enforced: true}
		b := &model.Constraint{Type: model.CheckConstraint, Definition: "CHECK (id > 1)", Enforced: true}
		assert.False(t, diff.ConstraintEqual(a, b))
	})
	t.Run("CHECK expression equal after canonicalisation", func(t *testing.T) {
		a := &model.Constraint{Type: model.CheckConstraint, Definition: "CHECK (`id` > 0)", Enforced: true}
		b := &model.Constraint{Type: model.CheckConstraint, Definition: "CHECK (id>0)", Enforced: true}
		assert.True(t, diff.ConstraintEqual(a, b))
	})
	t.Run("PK columns equal across casing/spacing/backticks", func(t *testing.T) {
		a := &model.Constraint{Type: model.PrimaryKeyConstraint, Definition: "(`id`, `n`)"}
		b := &model.Constraint{Type: model.PrimaryKeyConstraint, Definition: "(ID, N)"}
		assert.True(t, diff.ConstraintEqual(a, b))
	})
	t.Run("PK columns differ", func(t *testing.T) {
		a := &model.Constraint{Type: model.PrimaryKeyConstraint, Definition: "(id, n)"}
		b := &model.Constraint{Type: model.PrimaryKeyConstraint, Definition: "(id)"}
		assert.False(t, diff.ConstraintEqual(a, b))
	})
}

func TestIndexEqual(t *testing.T) {
	base := func() *model.Index {
		return &model.Index{
			Name:    "i_x",
			KeyType: "INDEX",
			Parts:   []model.IndexPart{{Column: "x"}},
		}
	}

	t.Run("identical", func(t *testing.T) {
		assert.True(t, diff.IndexEqual(base(), base()))
	})
	t.Run("key type differs", func(t *testing.T) {
		a, b := base(), base()
		b.KeyType = "UNIQUE"
		assert.False(t, diff.IndexEqual(a, b))
	})
	t.Run("part count differs", func(t *testing.T) {
		a, b := base(), base()
		b.Parts = append(b.Parts, model.IndexPart{Column: "y"})
		assert.False(t, diff.IndexEqual(a, b))
	})
	t.Run("part column differs", func(t *testing.T) {
		a, b := base(), base()
		b.Parts = []model.IndexPart{{Column: "y"}}
		assert.False(t, diff.IndexEqual(a, b))
	})
	t.Run("part length differs", func(t *testing.T) {
		a, b := base(), base()
		b.Parts = []model.IndexPart{{Column: "x", Length: 16}}
		assert.False(t, diff.IndexEqual(a, b))
	})
	t.Run("part DESC differs", func(t *testing.T) {
		a, b := base(), base()
		b.Parts = []model.IndexPart{{Column: "x", Desc: true}}
		assert.False(t, diff.IndexEqual(a, b))
	})
	t.Run("BTREE vs empty index type compare equal", func(t *testing.T) {
		a, b := base(), base()
		a.IndexType = "BTREE"
		b.IndexType = ""
		assert.True(t, diff.IndexEqual(a, b))
	})
	t.Run("HASH vs BTREE differ", func(t *testing.T) {
		a, b := base(), base()
		a.IndexType = "HASH"
		assert.False(t, diff.IndexEqual(a, b))
	})
	t.Run("invisible differs", func(t *testing.T) {
		a, b := base(), base()
		b.Invisible = true
		assert.False(t, diff.IndexEqual(a, b))
	})
	t.Run("comment differs", func(t *testing.T) {
		a, b := base(), base()
		ca, cb := "old", "new"
		a.Comment, b.Comment = &ca, &cb
		assert.False(t, diff.IndexEqual(a, b))
	})
	t.Run("comment added on one side", func(t *testing.T) {
		a, b := base(), base()
		c := "added"
		a.Comment = &c // b stays nil
		assert.False(t, diff.IndexEqual(a, b))
	})
	t.Run("identical comment compare equal", func(t *testing.T) {
		a, b := base(), base()
		ca, cb := "same", "same"
		a.Comment, b.Comment = &ca, &cb
		assert.True(t, diff.IndexEqual(a, b))
	})
}

func TestFKEqual(t *testing.T) {
	base := func() *model.ForeignKey {
		return &model.ForeignKey{
			Name:     "fk",
			Columns:  []string{"a"},
			RefDB:    "db",
			RefTable: "parent",
			RefCols:  []string{"id"},
		}
	}

	t.Run("identical", func(t *testing.T) {
		assert.True(t, diff.FKEqual(base(), base()))
	})
	t.Run("columns differ", func(t *testing.T) {
		a, b := base(), base()
		b.Columns = []string{"b"}
		assert.False(t, diff.FKEqual(a, b))
	})
	t.Run("ref cols differ", func(t *testing.T) {
		a, b := base(), base()
		b.RefCols = []string{"id2"}
		assert.False(t, diff.FKEqual(a, b))
	})
	t.Run("ref DB differs", func(t *testing.T) {
		a, b := base(), base()
		b.RefDB = "other"
		assert.False(t, diff.FKEqual(a, b))
	})
	t.Run("ref table differs", func(t *testing.T) {
		a, b := base(), base()
		b.RefTable = "other"
		assert.False(t, diff.FKEqual(a, b))
	})
	t.Run("ON DELETE differs", func(t *testing.T) {
		a, b := base(), base()
		b.OnDelete = "CASCADE"
		assert.False(t, diff.FKEqual(a, b))
	})
	t.Run("ON UPDATE differs", func(t *testing.T) {
		a, b := base(), base()
		b.OnUpdate = "SET NULL"
		assert.False(t, diff.FKEqual(a, b))
	})
	t.Run("MATCH type differs", func(t *testing.T) {
		a, b := base(), base()
		b.MatchType = "FULL"
		assert.False(t, diff.FKEqual(a, b))
	})
}

func TestAllPartsDropped(t *testing.T) {
	idx := func(parts ...model.IndexPart) *model.Index {
		return &model.Index{Parts: parts}
	}

	t.Run("empty parts", func(t *testing.T) {
		assert.False(t, diff.AllPartsDropped(idx(), nil))
	})
	t.Run("all column parts dropped", func(t *testing.T) {
		dropped := map[string]bool{"a": true, "b": true}
		assert.True(t, diff.AllPartsDropped(idx(model.IndexPart{Column: "a"}, model.IndexPart{Column: "b"}), dropped))
	})
	t.Run("one part not dropped", func(t *testing.T) {
		dropped := map[string]bool{"a": true}
		assert.False(t, diff.AllPartsDropped(idx(model.IndexPart{Column: "a"}, model.IndexPart{Column: "b"}), dropped))
	})
	t.Run("expression part disqualifies", func(t *testing.T) {
		dropped := map[string]bool{"a": true}
		// Expression parts have Column="" — even if every other column is dropped, return false.
		assert.False(t, diff.AllPartsDropped(idx(model.IndexPart{Column: "a"}, model.IndexPart{Column: ""}), dropped))
	})
}

func TestAddConstraintSQL_PrimaryKey(t *testing.T) {
	c := &model.Constraint{Type: model.PrimaryKeyConstraint, Definition: "(id)"}
	got := diff.AddConstraintSQL("t", c)
	assert.Equal(t, "ALTER TABLE t ADD PRIMARY KEY (id);", got)
}

func TestAddConstraintSQL_Check(t *testing.T) {
	t.Run("enforced", func(t *testing.T) {
		c := &model.Constraint{Type: model.CheckConstraint, Name: "chk_pos", Definition: "CHECK (id > 0)", Enforced: true}
		got := diff.AddConstraintSQL("t", c)
		assert.Equal(t, "ALTER TABLE t ADD CONSTRAINT chk_pos CHECK (id > 0);", got)
	})
	t.Run("not enforced", func(t *testing.T) {
		// Enforced=false appends the NOT ENFORCED suffix; pins the
		// emitter side of the round-trip with catalog.loadCheckConstraints
		// reading tc.ENFORCED.
		c := &model.Constraint{Type: model.CheckConstraint, Name: "chk_pos", Definition: "CHECK (id > 0)", Enforced: false}
		got := diff.AddConstraintSQL("t", c)
		assert.Equal(t, "ALTER TABLE t ADD CONSTRAINT chk_pos CHECK (id > 0) NOT ENFORCED;", got)
	})
}

func TestDropConstraintSQL_PrimaryKey(t *testing.T) {
	c := &model.Constraint{Type: model.PrimaryKeyConstraint, Name: "PRIMARY"}
	got := diff.DropConstraintSQL("t", c)
	assert.Equal(t, "ALTER TABLE t DROP PRIMARY KEY;", got)
}

func TestDropConstraintSQL_Check(t *testing.T) {
	c := &model.Constraint{Type: model.CheckConstraint, Name: "chk_pos"}
	got := diff.DropConstraintSQL("t", c)
	assert.Equal(t, "ALTER TABLE t DROP CHECK chk_pos;", got)
}

func TestTableCharsetCollationSQL(t *testing.T) {
	mk := func(charset, coll *string) *model.Table {
		return &model.Table{Charset: charset, Collation: coll}
	}
	utf8 := "utf8mb4"
	latin1 := "latin1"
	ai := "utf8mb4_0900_ai_ci"
	bin := "utf8mb4_bin"

	t.Run("both nil → empty", func(t *testing.T) {
		assert.Empty(t, diff.TableCharsetCollationSQL("t", mk(nil, nil), mk(nil, nil)))
	})
	t.Run("desired nil/nil short-circuit even if current set", func(t *testing.T) {
		// desired Charset=nil, Collation=nil means "user didn't declare
		// anything"; short-circuit returns empty regardless of current.
		assert.Empty(t, diff.TableCharsetCollationSQL("t", mk(&utf8, &ai), mk(nil, nil)))
	})
	t.Run("charset only, equal", func(t *testing.T) {
		assert.Empty(t, diff.TableCharsetCollationSQL("t", mk(&utf8, nil), mk(&utf8, nil)))
	})
	t.Run("charset only, differs", func(t *testing.T) {
		got := diff.TableCharsetCollationSQL("t", mk(&utf8, nil), mk(&latin1, nil))
		assert.Equal(t, "ALTER TABLE t DEFAULT CHARSET=latin1;", got)
	})
	t.Run("collation only, differs", func(t *testing.T) {
		got := diff.TableCharsetCollationSQL("t", mk(nil, &ai), mk(nil, &bin))
		assert.Equal(t, "ALTER TABLE t COLLATE=utf8mb4_bin;", got)
	})
	t.Run("charset+collation, both differ", func(t *testing.T) {
		got := diff.TableCharsetCollationSQL("t", mk(&latin1, nil), mk(&utf8, &ai))
		assert.Equal(t, "ALTER TABLE t DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;", got)
	})
	t.Run("desired Charset != nil, Collation nil; current Collation nil → equal", func(t *testing.T) {
		// "use the charset's default collation" — catalog represents
		// that as nil, so equal when current.Collation is also nil.
		assert.Empty(t, diff.TableCharsetCollationSQL("t", mk(&utf8, nil), mk(&utf8, nil)))
	})
	t.Run("desired Charset, no Collation; current has explicit non-default Collation → fires", func(t *testing.T) {
		got := diff.TableCharsetCollationSQL("t", mk(&utf8, &bin), mk(&utf8, nil))
		assert.Equal(t, "ALTER TABLE t DEFAULT CHARSET=utf8mb4;", got)
	})
	t.Run("convert-charset directive emits CONVERT TO when charset differs", func(t *testing.T) {
		current := mk(&latin1, nil)
		desired := mk(&utf8, &ai)
		desired.ConvertCharset = true
		got := diff.TableCharsetCollationSQL("t", current, desired)
		assert.Equal(t, "ALTER TABLE t CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;", got)
	})
	t.Run("convert-charset directive: collation-only diff falls through", func(t *testing.T) {
		// Charset matches, only Collation differs → CONVERT TO would
		// needlessly rebuild the table. Falls through to DEFAULT
		// CHARSET=…  COLLATE=… (full shape because parser requires
		// DEFAULT CHARSET on the directive's CREATE TABLE).
		current := mk(&utf8, &bin)
		desired := mk(&utf8, &ai)
		desired.ConvertCharset = true
		got := diff.TableCharsetCollationSQL("t", current, desired)
		assert.Equal(t, "ALTER TABLE t DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;", got)
	})
}

func TestTableCommentSQL(t *testing.T) {
	mk := func(c *string) *model.Table { return &model.Table{Comment: c} }
	old, new_ := "old", "new"
	empty := ""

	t.Run("both nil → empty", func(t *testing.T) {
		assert.Empty(t, diff.TableCommentSQL("t", mk(nil), mk(nil)))
	})
	t.Run("change", func(t *testing.T) {
		assert.Equal(t,
			"ALTER TABLE t COMMENT='new';",
			diff.TableCommentSQL("t", mk(&old), mk(&new_)))
	})
	t.Run("add", func(t *testing.T) {
		assert.Equal(t,
			"ALTER TABLE t COMMENT='new';",
			diff.TableCommentSQL("t", mk(nil), mk(&new_)))
	})
	t.Run("drop emits empty literal", func(t *testing.T) {
		assert.Equal(t,
			"ALTER TABLE t COMMENT='';",
			diff.TableCommentSQL("t", mk(&old), mk(nil)))
	})
	t.Run("desired explicit empty folded to nil → no diff", func(t *testing.T) {
		// canonicalComment folds &"" to nil so an explicit empty
		// COMMENT in desired SQL converges against catalog-side nil.
		assert.Empty(t, diff.TableCommentSQL("t", mk(nil), mk(&empty)))
	})
}

func TestCanonicalComment(t *testing.T) {
	empty := ""
	val := "x"
	assert.Nil(t, diff.CanonicalComment(nil))
	assert.Nil(t, diff.CanonicalComment(&empty))
	got := diff.CanonicalComment(&val)
	require.NotNil(t, got)
	assert.Equal(t, "x", *got)
}

func TestUniqueKeyPartitionCoverGap(t *testing.T) {
	mk := func(idxs ...*model.Index) *model.Table {
		tbl := &model.Table{
			Indexes: orderedmap.New[string, *model.Index](),
		}
		for _, i := range idxs {
			tbl.Indexes.Set(i.Name, i)
		}
		return tbl
	}
	pk := func(cols ...string) *model.Index {
		parts := make([]model.IndexPart, len(cols))
		for i, c := range cols {
			parts[i] = model.IndexPart{Column: c}
		}
		return &model.Index{Name: "PRIMARY", Primary: true, Parts: parts}
	}
	uniq := func(name string, cols ...string) *model.Index {
		parts := make([]model.IndexPart, len(cols))
		for i, c := range cols {
			parts[i] = model.IndexPart{Column: c}
		}
		return &model.Index{Name: name, KeyType: model.IndexUnique, Parts: parts}
	}

	t.Run("no required columns short-circuits", func(t *testing.T) {
		assert.Empty(t, diff.UniqueKeyPartitionCoverGap(mk(pk("id")), nil))
	})
	t.Run("PK covers required column", func(t *testing.T) {
		assert.Empty(t, diff.UniqueKeyPartitionCoverGap(mk(pk("id", "yr")), []string{"yr"}))
	})
	t.Run("PK missing partition column", func(t *testing.T) {
		got := diff.UniqueKeyPartitionCoverGap(mk(pk("id")), []string{"yr"})
		assert.Contains(t, got, "PRIMARY KEY")
		assert.Contains(t, got, "yr")
	})
	t.Run("UNIQUE missing partition column", func(t *testing.T) {
		got := diff.UniqueKeyPartitionCoverGap(mk(pk("id"), uniq("u_email", "email")), []string{"yr"})
		// PRIMARY is reported first because index iteration follows
		// declaration order (PRIMARY was set first).
		assert.Contains(t, got, "missing partition column")
	})
	t.Run("non-unique non-primary index ignored", func(t *testing.T) {
		// A plain KEY (not unique, not primary) doesn't constrain
		// partition coverage; only PRIMARY / UNIQUE matter.
		idx := &model.Index{Name: "idx", Parts: []model.IndexPart{{Column: "id"}}}
		assert.Empty(t, diff.UniqueKeyPartitionCoverGap(mk(idx), []string{"yr"}))
	})
	t.Run("case-insensitive column match", func(t *testing.T) {
		// required is lower-cased upstream; index parts may carry
		// any case from desired SQL. Match should fold both.
		assert.Empty(t, diff.UniqueKeyPartitionCoverGap(mk(pk("ID", "YR")), []string{"yr"}))
	})
}

func TestPartitionRequiredColumns(t *testing.T) {
	t.Run("empty clause → nil", func(t *testing.T) {
		got, err := diff.PartitionRequiredColumns("")
		require.NoError(t, err)
		assert.Nil(t, got)
	})
	t.Run("simple column reference", func(t *testing.T) {
		got, err := diff.PartitionRequiredColumns("partition by range (yr) (partition p0 values less than (2021))")
		require.NoError(t, err)
		assert.Equal(t, []string{"yr"}, got)
	})
	t.Run("function-wrapped column extracted", func(t *testing.T) {
		// PARTITION BY RANGE (YEAR(dt)) — only `dt` matters; YEAR
		// is a function name, not a column.
		got, err := diff.PartitionRequiredColumns("partition by range (year(dt)) (partition p0 values less than (2021))")
		require.NoError(t, err)
		assert.Equal(t, []string{"dt"}, got)
	})
	t.Run("multi-column dedup case-folded", func(t *testing.T) {
		got, err := diff.PartitionRequiredColumns("partition by range columns (a, B, a) (partition p0 values less than (1, 2, 3))")
		require.NoError(t, err)
		// Order preserves first-occurrence; case folded; dedup.
		assert.Equal(t, []string{"a", "b"}, got)
	})
	t.Run("invalid clause surfaces parse error", func(t *testing.T) {
		_, err := diff.PartitionRequiredColumns("not a partition clause")
		require.Error(t, err)
	})
}
