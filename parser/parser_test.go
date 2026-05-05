package parser_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"vitess.io/vitess/go/vt/sqlparser"

	"github.com/winebarrel/myschema/model"
	"github.com/winebarrel/myschema/parser"
)

func TestParseCreateTableBasics(t *testing.T) {
	sql := `
CREATE TABLE users (
    id BIGINT NOT NULL AUTO_INCREMENT,
    email VARCHAR(255) NOT NULL,
    name VARCHAR(64),
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (id),
    UNIQUE KEY users_email_key (email)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
`
	r, err := parser.ParseSQL(sql, "app")
	require.NoError(t, err)

	tbl, ok := r.Tables.GetOk("app.users")
	require.True(t, ok, "table app.users should be parsed")
	assert.Equal(t, "users", tbl.Name)
	assert.Equal(t, "app", tbl.Database)

	id, _ := tbl.Columns.GetOk("id")
	require.NotNil(t, id)
	assert.Equal(t, "bigint", id.TypeName)
	assert.True(t, id.NotNull)
	assert.True(t, id.AutoIncrement)

	email, _ := tbl.Columns.GetOk("email")
	require.NotNil(t, email)
	assert.Equal(t, "varchar(255)", email.TypeName)

	pk, ok := tbl.Constraints.GetOk("PRIMARY")
	require.True(t, ok)
	assert.Equal(t, []string{"id"}, pk.Columns)

	idx, ok := tbl.Indexes.GetOk("users_email_key")
	require.True(t, ok)
	assert.Equal(t, "UNIQUE", string(idx.KeyType))

	require.NotNil(t, tbl.Engine)
	assert.Equal(t, "InnoDB", *tbl.Engine)
}

func TestParseForeignKey(t *testing.T) {
	sql := `
CREATE TABLE users (id BIGINT NOT NULL, PRIMARY KEY (id));
CREATE TABLE posts (
    id BIGINT NOT NULL,
    user_id BIGINT NOT NULL,
    PRIMARY KEY (id),
    CONSTRAINT fk_posts_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE ON UPDATE SET NULL
);
`
	r, err := parser.ParseSQL(sql, "app")
	require.NoError(t, err)
	posts, ok := r.Tables.GetOk("app.posts")
	require.True(t, ok)
	fk, ok := posts.ForeignKeys.GetOk("fk_posts_user")
	require.True(t, ok)
	assert.Equal(t, []string{"user_id"}, fk.Columns)
	assert.Equal(t, "users", fk.RefTable)
	assert.Equal(t, "app", fk.RefDB)
	assert.Equal(t, "CASCADE", fk.OnDelete)
	assert.Equal(t, "SET NULL", fk.OnUpdate)
}

// TestParseForeignKeyMatchType pins the MATCH FULL / PARTIAL /
// SIMPLE branches in buildFK. Vitess parses the MATCH clause as
// sqlparser.{Full,Partial,Simple} and buildFK maps each to the
// model's MatchType string; the existing TestParseForeignKey only
// exercises the empty (no MATCH clause) path.
func TestParseForeignKeyMatchType(t *testing.T) {
	tests := []struct{ clause, want string }{
		{"MATCH FULL", "FULL"},
		{"MATCH PARTIAL", "PARTIAL"},
		{"MATCH SIMPLE", "SIMPLE"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			sql := `
CREATE TABLE users (id BIGINT NOT NULL, PRIMARY KEY (id));
CREATE TABLE posts (
    id BIGINT NOT NULL,
    user_id BIGINT NOT NULL,
    PRIMARY KEY (id),
    CONSTRAINT fk FOREIGN KEY (user_id) REFERENCES users(id) ` + tt.clause + `
);`
			r, err := parser.ParseSQL(sql, "app")
			require.NoError(t, err)
			posts, _ := r.Tables.GetOk("app.posts")
			fk, ok := posts.ForeignKeys.GetOk("fk")
			require.True(t, ok)
			assert.Equal(t, tt.want, fk.MatchType)
		})
	}
}

// TestParseInlineForeignKey checks the column-level `REFERENCES other(col)`
// shorthand: parser should auto-name the FK as `<table>_ibfk_<col>`.
func TestParseInlineForeignKey(t *testing.T) {
	sql := `
CREATE TABLE users (id BIGINT NOT NULL, PRIMARY KEY (id));
CREATE TABLE posts (
    id BIGINT NOT NULL,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    PRIMARY KEY (id)
);
`
	r, err := parser.ParseSQL(sql, "app")
	require.NoError(t, err)
	posts, _ := r.Tables.GetOk("app.posts")
	fk, ok := posts.ForeignKeys.GetOk("posts_ibfk_user_id")
	require.True(t, ok, "auto-named FK should exist")
	assert.Equal(t, "users", fk.RefTable)
	assert.Equal(t, "CASCADE", fk.OnDelete)
}

// TestParseInlineColumnPrimaryKey: vitess parses `id INT PRIMARY KEY`
// as a column with KeyOpt = ColKeyPrimary, distinct from a table-level
// PRIMARY KEY (id) constraint. The parser must promote the inline form
// to the same `t.Constraints["PRIMARY"]` + `t.Indexes["PRIMARY"]` shape
// the table-level path produces, otherwise the PK silently drops on
// apply (the regression that motivated this test). MySQL forces
// NOT NULL on a PK column even when the user wrote just `PRIMARY KEY`,
// so the column's NotNull flag must also be set — match the table-level
// behaviour in addIndex's IndexTypePrimary case.
func TestParseInlineColumnPrimaryKey(t *testing.T) {
	t.Run("explicit NOT NULL", func(t *testing.T) {
		sql := `CREATE TABLE t (id INT NOT NULL PRIMARY KEY, name VARCHAR(64));`
		r, err := parser.ParseSQL(sql, "app")
		require.NoError(t, err)
		tbl, _ := r.Tables.GetOk("app.t")
		require.NotNil(t, tbl)

		pk, ok := tbl.Constraints.GetOk("PRIMARY")
		require.True(t, ok, "inline PRIMARY KEY must produce a PRIMARY constraint")
		assert.Equal(t, model.PrimaryKeyConstraint, pk.Type)
		assert.Equal(t, []string{"id"}, pk.Columns)

		idx, ok := tbl.Indexes.GetOk("PRIMARY")
		require.True(t, ok, "inline PRIMARY KEY must produce a PRIMARY index")
		assert.True(t, idx.Primary)

		id, _ := tbl.Columns.GetOk("id")
		require.NotNil(t, id)
		assert.True(t, id.NotNull, "PK column must be NOT NULL")
	})
	t.Run("PRIMARY KEY without explicit NOT NULL forces NotNull", func(t *testing.T) {
		// MySQL silently makes the column NOT NULL when it's a PK; the
		// parser must mirror that so the dump round-trip stays stable.
		sql := `CREATE TABLE t (id INT PRIMARY KEY);`
		r, err := parser.ParseSQL(sql, "app")
		require.NoError(t, err)
		tbl, _ := r.Tables.GetOk("app.t")
		id, _ := tbl.Columns.GetOk("id")
		require.NotNil(t, id)
		assert.True(t, id.NotNull, "inline PK must force NotNull on its column")
	})
	t.Run("AUTO_INCREMENT combines with inline PRIMARY KEY", func(t *testing.T) {
		// `BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY` is one of the
		// most common real-world shapes for surrogate-key tables. It
		// should produce a PK constraint and preserve AUTO_INCREMENT
		// on the column — both attributes are independent in vitess
		// (KeyOpt and Autoincrement live on different fields), so the
		// promotion must not clobber AutoIncrement.
		sql := `CREATE TABLE t (id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY);`
		r, err := parser.ParseSQL(sql, "app")
		require.NoError(t, err)
		tbl, _ := r.Tables.GetOk("app.t")

		_, ok := tbl.Constraints.GetOk("PRIMARY")
		require.True(t, ok, "inline PK must produce PRIMARY constraint")

		id, _ := tbl.Columns.GetOk("id")
		require.NotNil(t, id)
		assert.True(t, id.NotNull)
		assert.True(t, id.AutoIncrement, "AUTO_INCREMENT must survive PK promotion")
	})
	t.Run("two inline PRIMARY KEYs: first column wins", func(t *testing.T) {
		// `CREATE TABLE t (a INT PRIMARY KEY, b INT PRIMARY KEY)` is
		// invalid MySQL (multiple PRIMARY KEYs), but vitess parses it
		// without an error — every column carries ColKeyPrimary
		// independently. applyInlineColumnKey's idempotent skip drops
		// the second promotion so the parsed model stays well-shaped:
		// the first column wins, MySQL still rejects at apply time.
		// Pin the precedence here so a refactor that flips the order
		// (or removes the skip) surfaces a regression in CI rather
		// than at apply.
		sql := `CREATE TABLE t (a INT PRIMARY KEY, b INT PRIMARY KEY);`
		r, err := parser.ParseSQL(sql, "app")
		require.NoError(t, err)
		tbl, _ := r.Tables.GetOk("app.t")

		pk, ok := tbl.Constraints.GetOk("PRIMARY")
		require.True(t, ok)
		assert.Equal(t, []string{"a"}, pk.Columns, "first column with inline PK wins")

		// Only column `a` gets NotNull forced — by its own
		// promotion. Column `b` hits the skip branch and is left
		// untouched: its NotNull flag stays at whatever vitess
		// parsed (here, the implicit nullable default for INT,
		// since the source SQL doesn't write NOT NULL on `b`).
		// The relevant assertion for this test is that the PK
		// column list contains only `a`; don't over-pin column
		// `b`'s NotNull flag.
	})
}

// TestParseInlineColumnUnique: `email VARCHAR(255) UNIQUE` and the
// `UNIQUE KEY` variant should both promote to a UNIQUE Index named
// after the column (matching MySQL's auto-naming and the table-level
// path's behaviour in addIndex's IndexTypeUnique case). Without the
// promotion the constraint silently drops on apply.
func TestParseInlineColumnUnique(t *testing.T) {
	for _, kw := range []string{"UNIQUE", "UNIQUE KEY"} {
		t.Run(kw, func(t *testing.T) {
			sql := `CREATE TABLE t (
    id INT NOT NULL,
    email VARCHAR(255) ` + kw + `,
    PRIMARY KEY (id)
);`
			r, err := parser.ParseSQL(sql, "app")
			require.NoError(t, err)
			tbl, _ := r.Tables.GetOk("app.t")
			require.NotNil(t, tbl)

			idx, ok := tbl.Indexes.GetOk("email")
			require.True(t, ok, "inline UNIQUE must produce a UNIQUE index named after the column")
			assert.Equal(t, model.IndexUnique, idx.KeyType)
			require.Len(t, idx.Parts, 1)
			assert.Equal(t, "email", idx.Parts[0].Column)
		})
	}
}

func TestParseCreateIndex(t *testing.T) {
	sql := `
CREATE TABLE users (id BIGINT NOT NULL, name VARCHAR(64), PRIMARY KEY (id));
CREATE INDEX idx_users_name ON users (name);
`
	r, err := parser.ParseSQL(sql, "app")
	require.NoError(t, err)
	users, _ := r.Tables.GetOk("app.users")
	idx, ok := users.Indexes.GetOk("idx_users_name")
	require.True(t, ok)
	require.Len(t, idx.Parts, 1)
	assert.Equal(t, "name", idx.Parts[0].Column)
}

// TestParseColumnAttributes covers the long tail of column options that
// have no dedicated test yet — DEFAULT, ON UPDATE, COMMENT, COLLATE,
// GENERATED.
func TestParseColumnAttributes(t *testing.T) {
	sql := `
CREATE TABLE wide (
    id BIGINT NOT NULL AUTO_INCREMENT,
    status VARCHAR(16) NOT NULL DEFAULT 'pending',
    rate DECIMAL(10,2) NOT NULL DEFAULT 0.00,
    description TEXT COLLATE utf8mb4_general_ci,
    note VARCHAR(64) COMMENT 'free-text note',
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    full_name VARCHAR(128) GENERATED ALWAYS AS (CONCAT(first, ' ', last)) STORED,
    PRIMARY KEY (id)
);
`
	r, err := parser.ParseSQL(sql, "app")
	require.NoError(t, err)
	tbl, _ := r.Tables.GetOk("app.wide")

	status, _ := tbl.Columns.GetOk("status")
	require.NotNil(t, status.Default)
	assert.Equal(t, "'pending'", *status.Default)

	rate, _ := tbl.Columns.GetOk("rate")
	require.NotNil(t, rate.Default)
	assert.Equal(t, "0.00", *rate.Default)

	desc, _ := tbl.Columns.GetOk("description")
	require.NotNil(t, desc.Collation)
	assert.Equal(t, "utf8mb4_general_ci", *desc.Collation)

	note, _ := tbl.Columns.GetOk("note")
	require.NotNil(t, note.Comment)
	assert.Equal(t, "free-text note", *note.Comment)

	updated, _ := tbl.Columns.GetOk("updated_at")
	require.NotNil(t, updated.Default)
	require.NotNil(t, updated.OnUpdate)

	full, _ := tbl.Columns.GetOk("full_name")
	require.NotNil(t, full.Generated)
	assert.True(t, full.Stored, "STORED generated column")
}

// TestParseColumnDefaultNullSkipped pins the parser-side fix for
// the "TIMESTAMP NULL DEFAULT NULL drift" gap: an explicit
// `DEFAULT NULL` (vitess hands it back as *sqlparser.NullVal) must
// fold to Column.Default=nil so the parser side matches the catalog
// reader, which leaves Default=nil for SQL-NULL COLUMN_DEFAULT.
// Covers all relevant types since the path is type-agnostic.
func TestParseColumnDefaultNullSkipped(t *testing.T) {
	sql := `
CREATE TABLE t (
    id INT NOT NULL,
    ts        TIMESTAMP NULL DEFAULT NULL,
    x_int     INT       DEFAULT NULL,
    x_str     VARCHAR(64) DEFAULT NULL,
    x_dec     DECIMAL(10,2) DEFAULT NULL,
    x_dt      DATETIME    DEFAULT NULL,
    PRIMARY KEY (id)
);`
	r, err := parser.ParseSQL(sql, "app")
	require.NoError(t, err)
	tbl, _ := r.Tables.GetOk("app.t")

	for _, col := range []string{"ts", "x_int", "x_str", "x_dec", "x_dt"} {
		t.Run(col, func(t *testing.T) {
			c, ok := tbl.Columns.GetOk(col)
			require.True(t, ok)
			assert.Nil(t, c.Default, "explicit DEFAULT NULL must fold to nil")
		})
	}
}

// TestParseColumnDefaultLiteralPreserved is the regression guard
// for the NullVal-skip: literal defaults (numbers, strings, function
// calls) must continue to round-trip — only the *NullVal case is
// special-cased.
func TestParseColumnDefaultLiteralPreserved(t *testing.T) {
	sql := `
CREATE TABLE t (
    id INT NOT NULL,
    n   INT NOT NULL DEFAULT 0,
    s   VARCHAR(16) NOT NULL DEFAULT 'pending',
    ts  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (id)
);`
	r, err := parser.ParseSQL(sql, "app")
	require.NoError(t, err)
	tbl, _ := r.Tables.GetOk("app.t")

	n, _ := tbl.Columns.GetOk("n")
	require.NotNil(t, n.Default)
	assert.Equal(t, "0", *n.Default)

	s, _ := tbl.Columns.GetOk("s")
	require.NotNil(t, s.Default)
	assert.Equal(t, "'pending'", *s.Default)

	ts, _ := tbl.Columns.GetOk("ts")
	require.NotNil(t, ts.Default)
	assert.Equal(t, "CURRENT_TIMESTAMP", *ts.Default)
}

// TestParseSpatialAndFulltextIndex exercises the two index categories that
// the catalog reader maps via INDEX_TYPE; without them we'd have a regression
// gap if the catalog renamed those types.
func TestParseSpatialAndFulltextIndex(t *testing.T) {
	sql := `
CREATE TABLE docs (
    id BIGINT NOT NULL,
    body TEXT,
    location POINT NOT NULL,
    PRIMARY KEY (id),
    FULLTEXT KEY ft_body (body),
    SPATIAL KEY sp_location (location)
);
`
	r, err := parser.ParseSQL(sql, "app")
	require.NoError(t, err)
	tbl, _ := r.Tables.GetOk("app.docs")

	ft, ok := tbl.Indexes.GetOk("ft_body")
	require.True(t, ok)
	assert.Equal(t, model.IndexFulltext, ft.KeyType)

	sp, ok := tbl.Indexes.GetOk("sp_location")
	require.True(t, ok)
	assert.Equal(t, model.IndexSpatial, sp.KeyType)
}

// TestParseIndexQuirks covers prefix length, DESC ordering, INVISIBLE,
// USING BTREE, and multi-column indexes — all things that round-trip via
// information_schema.STATISTICS columns.
func TestParseIndexQuirks(t *testing.T) {
	sql := `
CREATE TABLE quirks (
    id BIGINT NOT NULL,
    name VARCHAR(255) NOT NULL,
    other VARCHAR(64),
    PRIMARY KEY (id),
    KEY idx_name_prefix (name(20)),
    KEY idx_desc (name DESC),
    KEY idx_combined (name, other),
    KEY idx_invisible (other) INVISIBLE,
    KEY idx_using (other) USING BTREE
);
`
	r, err := parser.ParseSQL(sql, "app")
	require.NoError(t, err)
	tbl, _ := r.Tables.GetOk("app.quirks")

	prefix, _ := tbl.Indexes.GetOk("idx_name_prefix")
	require.NotNil(t, prefix)
	require.Len(t, prefix.Parts, 1)
	assert.Equal(t, 20, prefix.Parts[0].Length)

	desc, _ := tbl.Indexes.GetOk("idx_desc")
	require.NotNil(t, desc)
	assert.True(t, desc.Parts[0].Desc)

	combined, _ := tbl.Indexes.GetOk("idx_combined")
	require.NotNil(t, combined)
	require.Len(t, combined.Parts, 2)

	invisible, _ := tbl.Indexes.GetOk("idx_invisible")
	require.NotNil(t, invisible)
	assert.True(t, invisible.Invisible)

	using, _ := tbl.Indexes.GetOk("idx_using")
	require.NotNil(t, using)
	assert.Equal(t, "BTREE", using.IndexType)
}

// TestParseIndexComment exercises the COMMENT '…' option on every
// secondary-index shape addIndex handles — pre-fix, parser.addIndex
// inspected only the `using` and `invisible` options, so any
// user-written COMMENT was silently dropped (catalog round-trips it
// via INDEX_COMMENT, so dump → re-parse would also lose the comment).
// addIndex has four type-specific branches (KEY / UNIQUE / FULLTEXT /
// SPATIAL) that all need to thread Comment through; one test row per
// branch.
func TestParseIndexComment(t *testing.T) {
	sql := `
CREATE TABLE t (
    id INT NOT NULL,
    a  INT NOT NULL,
    body TEXT,
    location POINT NOT NULL,
    PRIMARY KEY (id),
    KEY idx_key (a) COMMENT 'plain key',
    UNIQUE KEY idx_unique (a) COMMENT 'unique key',
    FULLTEXT KEY idx_ft (body) COMMENT 'fulltext key',
    SPATIAL KEY idx_sp (location) COMMENT 'spatial key'
);
`
	r, err := parser.ParseSQL(sql, "app")
	require.NoError(t, err)
	tbl, _ := r.Tables.GetOk("app.t")

	for _, c := range []struct {
		name, want string
	}{
		{"idx_key", "plain key"},
		{"idx_unique", "unique key"},
		{"idx_ft", "fulltext key"},
		{"idx_sp", "spatial key"},
	} {
		t.Run(c.name, func(t *testing.T) {
			idx, ok := tbl.Indexes.GetOk(c.name)
			require.True(t, ok)
			require.NotNil(t, idx.Comment, "Comment must survive the parse")
			assert.Equal(t, c.want, *idx.Comment)
		})
	}
}

// TestParseIndexEmptyCommentFoldedToNil pins the corner case: an
// index declared with an *explicit* empty-string COMMENT clause
// (the user wrote `COMMENT` followed by an empty literal) must
// collapse to `Comment=nil` on the parser side, matching the
// catalog reader's normalisation of empty INDEX_COMMENT. Without
// this fold, a desired-side `KEY idx (col) COMMENT <empty>` would
// compare unequal to the catalog-side nil under indexEqual's
// ptrEq and `plan` would re-emit DROP+CREATE on every run.
func TestParseIndexEmptyCommentFoldedToNil(t *testing.T) {
	r, err := parser.ParseSQL(`
CREATE TABLE t (
    id INT NOT NULL,
    a  INT NOT NULL,
    PRIMARY KEY (id),
    KEY idx_a (a) COMMENT ''
);
`, "app")
	require.NoError(t, err)
	tbl, _ := r.Tables.GetOk("app.t")
	idx, _ := tbl.Indexes.GetOk("idx_a")
	assert.Nil(t, idx.Comment, `explicit COMMENT '' must fold to nil`)
}

// TestParseIndexNoComment pins the negative case: an index with
// no COMMENT clause must produce Comment=nil (matches the catalog
// reader's "empty information_schema.STATISTICS.INDEX_COMMENT → nil"
// normalisation, so steady state stays equal under indexEqual's
// ptrEq).
func TestParseIndexNoComment(t *testing.T) {
	r, err := parser.ParseSQL(`
CREATE TABLE t (
    id INT NOT NULL,
    a  INT NOT NULL,
    PRIMARY KEY (id),
    KEY idx_a (a)
);
`, "app")
	require.NoError(t, err)
	tbl, _ := r.Tables.GetOk("app.t")
	idx, _ := tbl.Indexes.GetOk("idx_a")
	assert.Nil(t, idx.Comment)
}

// TestParseStandaloneCreateIndexComment exercises the standalone
// `CREATE INDEX ... COMMENT '...'` path. vitess collapses the
// statement into an *AlterTable with an AddIndexDefinition option,
// which still routes through addIndex — so the COMMENT capture
// must work for both inline (CREATE TABLE body) and standalone
// shapes. Without this regression, a future refactor that splits
// the inline / standalone code paths could drop COMMENT on one
// side without surfacing the gap in fixture coverage.
func TestParseStandaloneCreateIndexComment(t *testing.T) {
	r, err := parser.ParseSQL(`
CREATE TABLE t (
    id INT NOT NULL,
    a  INT NOT NULL,
    PRIMARY KEY (id)
);
CREATE INDEX idx_a ON t (a) COMMENT 'standalone note';
`, "app")
	require.NoError(t, err)
	tbl, _ := r.Tables.GetOk("app.t")
	idx, ok := tbl.Indexes.GetOk("idx_a")
	require.True(t, ok)
	require.NotNil(t, idx.Comment)
	assert.Equal(t, "standalone note", *idx.Comment)
}

// TestParseDuplicateIndexNameRejected exercises the duplicate-index
// guard for every IndexType branch addIndex handles (KEY / UNIQUE /
// FULLTEXT / SPATIAL). MySQL rejects duplicate index names at
// CREATE TABLE time, so myschema must surface the conflict at parse
// time rather than letting an invalid model reach the diff layer.
func TestParseDuplicateIndexNameRejected(t *testing.T) {
	tests := []struct {
		name, body string
	}{
		{
			name: "plain KEY",
			body: "KEY idx (a), KEY idx (a)",
		},
		{
			name: "UNIQUE",
			body: "UNIQUE KEY idx (a), UNIQUE KEY idx (a)",
		},
		{
			name: "FULLTEXT",
			body: "FULLTEXT KEY idx (body), FULLTEXT KEY idx (body)",
		},
		{
			name: "SPATIAL",
			body: "SPATIAL KEY idx (location), SPATIAL KEY idx (location)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sql := `CREATE TABLE t (
    id INT NOT NULL,
    a INT NOT NULL,
    body TEXT NOT NULL,
    location POINT NOT NULL,
    PRIMARY KEY (id),
    ` + tt.body + `
);`
			_, err := parser.ParseSQL(sql, "app")
			require.Error(t, err)
			assert.Contains(t, err.Error(), "duplicate index: idx")
		})
	}
}

// TestParseCheckConstraint verifies both inline and ALTER TABLE ADD
// CONSTRAINT shapes for CHECK, including the NOT ENFORCED suffix.
func TestParseCheckConstraint(t *testing.T) {
	sql := `
CREATE TABLE products (
    id BIGINT NOT NULL,
    price DECIMAL(10,2) NOT NULL,
    PRIMARY KEY (id),
    CONSTRAINT chk_price_positive CHECK (price >= 0)
);
ALTER TABLE products ADD CONSTRAINT chk_price_capped CHECK (price <= 9999) NOT ENFORCED;
`
	r, err := parser.ParseSQL(sql, "app")
	require.NoError(t, err)
	tbl, _ := r.Tables.GetOk("app.products")

	c, ok := tbl.Constraints.GetOk("chk_price_positive")
	require.True(t, ok)
	assert.Equal(t, model.CheckConstraint, c.Type)
	assert.True(t, c.Enforced, "default CHECK is enforced")
	assert.Contains(t, c.Definition, "price")

	c2, ok := tbl.Constraints.GetOk("chk_price_capped")
	require.True(t, ok)
	assert.False(t, c2.Enforced, "NOT ENFORCED preserved")
}

// TestParseMultiColumnPrimaryKey ensures the constraint Columns list is
// preserved in declared order and PK columns get NOT NULL applied
// transitively.
func TestParseMultiColumnPrimaryKey(t *testing.T) {
	sql := `
CREATE TABLE links (
    src BIGINT,
    dst BIGINT,
    weight INT,
    PRIMARY KEY (src, dst)
);
`
	r, err := parser.ParseSQL(sql, "app")
	require.NoError(t, err)
	tbl, _ := r.Tables.GetOk("app.links")

	pk, _ := tbl.Constraints.GetOk("PRIMARY")
	assert.Equal(t, []string{"src", "dst"}, pk.Columns)

	src, _ := tbl.Columns.GetOk("src")
	assert.True(t, src.NotNull, "PK column should be implicitly NOT NULL")
	dst, _ := tbl.Columns.GetOk("dst")
	assert.True(t, dst.NotNull)
	weight, _ := tbl.Columns.GetOk("weight")
	assert.False(t, weight.NotNull, "non-PK column unaffected")
}

// TestParseViewWithColumnList exercises the optional column-alias list
// in `CREATE VIEW v (a, b) AS SELECT …`.
func TestParseViewWithColumnList(t *testing.T) {
	sql := `
CREATE TABLE t (id INT, n INT, PRIMARY KEY (id));
CREATE VIEW v (alias_id, alias_n) AS SELECT id, n FROM t;
`
	r, err := parser.ParseSQL(sql, "app")
	require.NoError(t, err)
	v, ok := r.Views.GetOk("app.v")
	require.True(t, ok)
	assert.Equal(t, []string{"alias_id", "alias_n"}, v.Cols)
}

// TestParseTableOptions covers ENGINE / CHARSET / COLLATE / COMMENT /
// AUTO_INCREMENT — all the table options model.Table understands.
func TestParseTableOptions(t *testing.T) {
	sql := `
CREATE TABLE t (id INT, PRIMARY KEY (id))
  ENGINE=InnoDB
  DEFAULT CHARSET=utf8mb4
  COLLATE=utf8mb4_0900_ai_ci
  AUTO_INCREMENT=100
  COMMENT='hello world';
`
	r, err := parser.ParseSQL(sql, "app")
	require.NoError(t, err)
	tbl, _ := r.Tables.GetOk("app.t")

	require.NotNil(t, tbl.Engine)
	assert.Equal(t, "InnoDB", *tbl.Engine)
	require.NotNil(t, tbl.Charset)
	assert.Equal(t, "utf8mb4", *tbl.Charset)
	// utf8mb4_0900_ai_ci is the MySQL 8.0 default collation for utf8mb4,
	// so it's collapsed to nil — the parser side mirrors what the
	// catalog side does after reading information_schema, keeping
	// `CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci` and bare
	// `CHARSET=utf8mb4` describing the same MySQL state.
	assert.Nil(t, tbl.Collation)
	require.NotNil(t, tbl.AutoIncrement)
	assert.Equal(t, uint64(100), *tbl.AutoIncrement)
	require.NotNil(t, tbl.Comment)
	assert.Equal(t, "hello world", *tbl.Comment)
}

func TestParseTableOptionsExplicitNonDefaultCollation(t *testing.T) {
	// A non-default collation (utf8mb4_unicode_ci is not utf8mb4's
	// default) survives parser-side normalisation.
	sql := `
CREATE TABLE t (id INT, PRIMARY KEY (id))
  DEFAULT CHARSET=utf8mb4
  COLLATE=utf8mb4_unicode_ci;
`
	r, err := parser.ParseSQL(sql, "app")
	require.NoError(t, err)
	tbl, _ := r.Tables.GetOk("app.t")
	require.NotNil(t, tbl.Collation)
	assert.Equal(t, "utf8mb4_unicode_ci", *tbl.Collation)
}

// TestParseColumnCharacterSet pins that vitess's column-level CHARACTER
// SET clause makes it onto model.Column. The vitess AST stores it on
// `cd.Type.Charset.Name` (a separate struct, not under
// cd.Type.Options), so a parser pass that only inspects `Options` —
// as an earlier version of parseColumnDef did — silently drops every
// per-column charset and the whole catalog-vs-parser comparison
// degenerates into endless MODIFY COLUMN drift.
func TestParseColumnCharacterSet(t *testing.T) {
	sql := `
CREATE TABLE t (
    id BIGINT NOT NULL,
    plain VARCHAR(64),
    explicit_cs VARCHAR(64) CHARACTER SET latin1,
    explicit_both VARCHAR(64) CHARACTER SET latin1 COLLATE latin1_bin,
    matches_default VARCHAR(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci,
    PRIMARY KEY (id)
) DEFAULT CHARSET=utf8mb4;
`
	r, err := parser.ParseSQL(sql, "app")
	require.NoError(t, err)
	tbl, _ := r.Tables.GetOk("app.t")

	plain, _ := tbl.Columns.GetOk("plain")
	assert.Nil(t, plain.CharacterSet, "no CHARACTER SET on column → nil")
	assert.Nil(t, plain.Collation)

	cs, _ := tbl.Columns.GetOk("explicit_cs")
	require.NotNil(t, cs.CharacterSet)
	assert.Equal(t, "latin1", *cs.CharacterSet, "explicit CHARACTER SET captured")
	// COLLATE not given → parser sets nil (catalog will fill it via
	// information_schema; both sides collapse it through CollapseDefault
	// so the comparison stays quiet).
	assert.Nil(t, cs.Collation)

	both, _ := tbl.Columns.GetOk("explicit_both")
	require.NotNil(t, both.CharacterSet)
	assert.Equal(t, "latin1", *both.CharacterSet)
	require.NotNil(t, both.Collation)
	assert.Equal(t, "latin1_bin", *both.Collation, "non-default collation survives")

	// Spelling out the charset's default collation is redundant; parser
	// collapses it to nil so it compares equal to the catalog side.
	matches, _ := tbl.Columns.GetOk("matches_default")
	require.NotNil(t, matches.CharacterSet)
	assert.Equal(t, "utf8mb4", *matches.CharacterSet)
	assert.Nil(t, matches.Collation, "default collation collapsed to nil")
}

// TestParseTableOptionsCollateOnly pins that a table whose only
// charset-related option is `COLLATE=…` (no DEFAULT CHARSET) still
// gets the same default-collation collapse as a CHARSET+COLLATE
// table. Without the collation→charset fallback in the parser pass,
// a redundantly-spelled default collation would survive on the
// parser side while the catalog side (which always knows the
// effective charset via information_schema) collapses it — endless
// ALTER TABLE … COLLATE=… loops on every plan.
func TestParseTableOptionsCollateOnly(t *testing.T) {
	t.Run("default collation collapses to nil", func(t *testing.T) {
		r, err := parser.ParseSQL(`CREATE TABLE t (id INT) COLLATE=utf8mb4_0900_ai_ci;`, "app")
		require.NoError(t, err)
		tbl, _ := r.Tables.GetOk("app.t")
		assert.Nil(t, tbl.Charset, "no DEFAULT CHARSET → Charset stays nil")
		assert.Nil(t, tbl.Collation, "default collation collapsed away")
	})
	t.Run("non-default collation survives", func(t *testing.T) {
		r, err := parser.ParseSQL(`CREATE TABLE t (id INT) COLLATE=utf8mb4_unicode_ci;`, "app")
		require.NoError(t, err)
		tbl, _ := r.Tables.GetOk("app.t")
		assert.Nil(t, tbl.Charset)
		require.NotNil(t, tbl.Collation)
		assert.Equal(t, "utf8mb4_unicode_ci", *tbl.Collation)
	})
}

// TestParseColumnCollateOnlyInheritsTableCharset pins that a column
// declared with `COLLATE …` and no `CHARACTER SET` participates in the
// same default-collation collapse as a column with the explicit
// charset — the effective charset is the table default, and a COLLATE
// that matches the table charset's default collation drops to nil so
// the parser side compares equal to the catalog side (which always
// resolves the effective charset before collapsing).
func TestParseColumnCollateOnlyInheritsTableCharset(t *testing.T) {
	sql := `
CREATE TABLE t (
    id BIGINT NOT NULL,
    redundant VARCHAR(64) COLLATE utf8mb4_0900_ai_ci,
    explicit VARCHAR(64) COLLATE utf8mb4_unicode_ci,
    PRIMARY KEY (id)
) DEFAULT CHARSET=utf8mb4;
`
	r, err := parser.ParseSQL(sql, "app")
	require.NoError(t, err)
	tbl, _ := r.Tables.GetOk("app.t")

	// The COLLATE matches utf8mb4's default → collapsed to nil.
	redundant, _ := tbl.Columns.GetOk("redundant")
	assert.Nil(t, redundant.CharacterSet, "COLLATE-only column has no explicit charset")
	assert.Nil(t, redundant.Collation, "default collation for the table-default charset collapses to nil")

	// A non-default collation on a COLLATE-only column survives.
	explicit, _ := tbl.Columns.GetOk("explicit")
	assert.Nil(t, explicit.CharacterSet)
	require.NotNil(t, explicit.Collation)
	assert.Equal(t, "utf8mb4_unicode_ci", *explicit.Collation)
}

// TestParseDuplicateRejection ensures the parser surfaces obvious mistakes
// rather than silently overwriting.
func TestParseDuplicateRejection(t *testing.T) {
	cases := map[string]string{
		"duplicate table": `
CREATE TABLE t (id INT, PRIMARY KEY (id));
CREATE TABLE t (id INT, PRIMARY KEY (id));`,
		"duplicate view": `
CREATE TABLE t (id INT, PRIMARY KEY (id));
CREATE VIEW v AS SELECT 1;
CREATE VIEW v AS SELECT 2;`,
		"duplicate column": `
CREATE TABLE t (id INT, id INT, PRIMARY KEY (id));`,
	}
	for name, sql := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := parser.ParseSQL(sql, "app")
			require.Error(t, err)
			assert.Contains(t, err.Error(), "duplicate")
		})
	}
}

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

func TestParseSQL_DirectiveConflicts(t *testing.T) {
	tests := []struct {
		name   string
		sql    string
		errSub string
	}{
		{
			name: "execute + statement-level rename rejected",
			sql: `-- myschema:execute SELECT 1
-- myschema:renamed-from old_t
CREATE TABLE t (id INT);`,
			errSub: "directives cannot be combined",
		},
		{
			name: "execute + inline rename rejected",
			sql: `-- myschema:execute SELECT 1
CREATE TABLE t (
    -- myschema:renamed-from old_id
    id INT
);`,
			errSub: "renamed-from cannot appear inside an execute payload",
		},
		{
			name: "execute + convert-charset rejected",
			sql: `-- myschema:execute SELECT 1
-- myschema:convert-charset
CREATE TABLE t (id INT) DEFAULT CHARSET=utf8mb4;`,
			errSub: "directives cannot be combined",
		},
		{
			name: "convert-charset + statement-level rename rejected",
			sql: `-- myschema:convert-charset
-- myschema:renamed-from old_t
CREATE TABLE t (id INT) DEFAULT CHARSET=utf8mb4;`,
			errSub: "directives cannot be combined",
		},
		{
			name: "convert-charset without DEFAULT CHARSET rejected",
			sql: `-- myschema:convert-charset
CREATE TABLE t (id INT);`,
			errSub: "requires the CREATE TABLE to declare a DEFAULT CHARSET",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parser.ParseSQL(tt.sql, "app")
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.errSub)
		})
	}
}

func TestParseSQL_ExecuteDirectiveValidation(t *testing.T) {
	tests := []struct {
		name   string
		sql    string
		errSub string
	}{
		{
			name: "missing payload after directive",
			sql: `-- myschema:execute SELECT 1
-- only comments
`,
			errSub: "missing the SQL statement",
		},
		{
			name: "check SQL is not a SELECT",
			sql: `-- myschema:execute INSERT INTO t VALUES (1)
SELECT 1;`,
			errSub: "must be a SELECT",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parser.ParseSQL(tt.sql, "app")
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.errSub)
		})
	}
}

func TestParseSQL_RejectsCrossDatabaseQualifier(t *testing.T) {
	// Regression: PR #61 made emitted DDL unqualified, so a desired-side
	// `CREATE TABLE other_db.users (...)` would silently apply to the
	// invocation database instead of `other_db`. Reject it at parse
	// time, with the same posture for CREATE VIEW and ALTER TABLE.
	tests := []struct {
		name   string
		sql    string
		errSub string
	}{
		{
			name:   "CREATE TABLE qualified to a different database",
			sql:    `CREATE TABLE other_db.users (id INT, PRIMARY KEY (id));`,
			errSub: `CREATE TABLE other_db.users: cross-database reference not supported`,
		},
		{
			name:   "CREATE VIEW qualified to a different database",
			sql:    `CREATE VIEW other_db.v AS SELECT 1;`,
			errSub: `CREATE VIEW other_db.v: cross-database reference not supported`,
		},
		{
			name: "ALTER TABLE qualified to a different database",
			sql: `CREATE TABLE users (id INT, PRIMARY KEY (id));
ALTER TABLE other_db.users ADD CONSTRAINT chk_id CHECK (id > 0);`,
			errSub: `ALTER TABLE other_db.users: cross-database reference not supported`,
		},
		{
			// vitess collapses CREATE INDEX into an AlterTable, so the
			// user-facing CREATE INDEX shape funnels through the same
			// validation — pin it so the rejection survives if vitess
			// ever stops doing the collapse.
			name: "CREATE INDEX qualified to a different database",
			sql: `CREATE TABLE users (id INT, PRIMARY KEY (id));
CREATE INDEX idx_id ON other_db.users (id);`,
			errSub: `ALTER TABLE other_db.users: cross-database reference not supported`,
		},
		{
			// Qualifier wrapped in backticks must still match: vitess
			// strips the quoting before exposing Qualifier.String(),
			// so the rejection fires the same way and the user can't
			// dodge the check with `other_db`.users.
			name:   "CREATE TABLE with back-ticked cross-database qualifier",
			sql:    "CREATE TABLE `other_db`.users (id INT, PRIMARY KEY (id));",
			errSub: `CREATE TABLE other_db.users: cross-database reference not supported`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parser.ParseSQL(tt.sql, "app")
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.errSub)
		})
	}
}

func TestParseSQL_AcceptsMatchingDatabaseQualifier(t *testing.T) {
	// Hand-written desired SQL that qualifies with the invocation
	// database is fine — qualifier == defaultDB collapses to the same
	// resolved Database, matching the dump-then-edit workflow where a
	// user might keep the qualifier they pasted from elsewhere.
	r, err := parser.ParseSQL(`CREATE TABLE app.users (id INT, PRIMARY KEY (id));`, "app")
	require.NoError(t, err)
	tbl, ok := r.Tables.GetOk("app.users")
	require.True(t, ok)
	assert.Equal(t, "app", tbl.Database)
	assert.Equal(t, "users", tbl.Name)
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

func TestParseSQL_TopLevelErrors(t *testing.T) {
	tests := []struct {
		name   string
		sql    string
		errSub string
	}{
		{
			name:   "duplicate table",
			sql:    "CREATE TABLE t (id INT); CREATE TABLE t (id INT);",
			errSub: "duplicate table",
		},
		{
			name:   "duplicate view",
			sql:    "CREATE TABLE t (id INT); CREATE VIEW v AS SELECT id FROM t; CREATE VIEW v AS SELECT id FROM t;",
			errSub: "duplicate view",
		},
		{
			name:   "duplicate column inside CREATE TABLE",
			sql:    "CREATE TABLE t (id INT, id INT);",
			errSub: "duplicate column",
		},
		{
			name:   "ALTER TABLE on unknown table",
			sql:    "ALTER TABLE nope ADD CONSTRAINT chk CHECK (1);",
			errSub: "ALTER TABLE on unknown table",
		},
		{
			name:   "SUBPARTITION rejected",
			sql:    "CREATE TABLE t (id INT, n INT, PRIMARY KEY (id, n)) PARTITION BY RANGE (n) SUBPARTITION BY HASH (id) (PARTITION p1 VALUES LESS THAN (10));",
			errSub: "SUBPARTITION",
		},
		{
			name:   "convert-charset on ALTER TABLE rejected",
			sql:    "CREATE TABLE t (id INT) DEFAULT CHARSET=utf8mb4;\n-- myschema:convert-charset\nALTER TABLE t ADD CONSTRAINT chk CHECK (id > 0);",
			errSub: "convert-charset",
		},
		{
			name:   "rename directive on CREATE VIEW rejected",
			sql:    "CREATE TABLE t (id INT);\n-- myschema:renamed-from old_v\nCREATE VIEW v AS SELECT id FROM t;",
			errSub: "renamed-from",
		},
		{
			name:   "rename directive on unsupported statement (CREATE DATABASE) rejected",
			sql:    "-- myschema:renamed-from old_db\nCREATE DATABASE x;",
			errSub: "renamed-from",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parser.ParseSQL(tt.sql, "app")
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.errSub)
		})
	}
}

func TestParseSQL_DuplicateForeignKey(t *testing.T) {
	// addTableConstraint surfaces duplicate FK names in the same CREATE
	// TABLE rather than letting the latter silently win.
	_, err := parser.ParseSQL(`
CREATE TABLE p (id INT NOT NULL, PRIMARY KEY (id));
CREATE TABLE c (
    id INT NOT NULL,
    a INT NOT NULL,
    b INT NOT NULL,
    PRIMARY KEY (id),
    CONSTRAINT fk FOREIGN KEY (a) REFERENCES p (id),
    CONSTRAINT fk FOREIGN KEY (b) REFERENCES p (id),
    KEY ka (a),
    KEY kb (b)
);
`, "app")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate foreign key")
}

func TestParseSQL_UnparseableInputErrors(t *testing.T) {
	// SplitStatementToPieces tolerates many shapes, but a totally
	// malformed statement still surfaces as a parse error.
	_, err := parser.ParseSQL("CREATE TABLE )))) garbage (", "app")
	require.Error(t, err)
}

func TestParseSQL_ValidateDirectivesError(t *testing.T) {
	// An unknown directive prefix is rejected by ValidateDirectives
	// before any vitess parse runs — pin the early-out path.
	_, err := parser.ParseSQL(`-- myschema:bogus-directive
CREATE TABLE t (id INT);`, "app")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown myschema directive")
}

func TestParseSQL_EmptyPieceSkipped(t *testing.T) {
	// Consecutive semicolons split into an empty piece between two
	// real statements. The parser must skip the empty piece silently
	// (not fail with "parse SQL: empty statement") and still register
	// both real CREATE TABLEs.
	r, err := parser.ParseSQL("CREATE TABLE a (id INT);;CREATE TABLE b (id INT);", "app")
	require.NoError(t, err)
	_, okA := r.Tables.GetOk("app.a")
	_, okB := r.Tables.GetOk("app.b")
	assert.True(t, okA, "table a must be parsed despite the empty piece")
	assert.True(t, okB, "table b must be parsed despite the empty piece")
}
