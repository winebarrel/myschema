package parser_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
