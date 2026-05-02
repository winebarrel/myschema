package catalog_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/winebarrel/myschema/catalog"
	"github.com/winebarrel/myschema/internal/testutil"
	"github.com/winebarrel/myschema/model"
)

// All tests in this file require a reachable MySQL (start it with
// `docker compose up -d`). Each test recreates the test database via
// testutil.SetupDB so they're independent and can run in any order.

func TestTablesRoundTrip(t *testing.T) {
	db := testutil.ConnectDB(t)
	ctx := context.Background()

	testutil.SetupDB(t, ctx, db, `
CREATE TABLE users (
    id BIGINT NOT NULL AUTO_INCREMENT,
    email VARCHAR(255) NOT NULL,
    PRIMARY KEY (id),
    UNIQUE KEY users_email_key (email)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
`)

	cat := catalog.NewCatalog(db, testutil.DefaultDB)
	tables, err := cat.Tables(ctx)
	require.NoError(t, err)

	tbl, ok := tables.GetOk("myschema_test.users")
	require.True(t, ok, "users table should be present")

	id, ok := tbl.Columns.GetOk("id")
	require.True(t, ok)
	require.Equal(t, "bigint", id.TypeName)
	require.True(t, id.NotNull)
	require.True(t, id.AutoIncrement)

	idx, ok := tbl.Indexes.GetOk("users_email_key")
	require.True(t, ok)
	require.Equal(t, "UNIQUE", string(idx.KeyType))

	pk, ok := tbl.Constraints.GetOk("PRIMARY")
	require.True(t, ok)
	assert.Equal(t, model.PrimaryKeyConstraint, pk.Type)
	assert.Equal(t, []string{"id"}, pk.Columns)
}

// TestForeignKeyRoundTrip exercises loadForeignKeys: column list,
// referenced db/table/columns, and ON DELETE / ON UPDATE actions.
func TestForeignKeyRoundTrip(t *testing.T) {
	db := testutil.ConnectDB(t)
	ctx := context.Background()
	testutil.SetupDB(t, ctx, db, `
CREATE TABLE users (id BIGINT NOT NULL, PRIMARY KEY (id));
CREATE TABLE posts (
    id BIGINT NOT NULL,
    user_id BIGINT,  -- nullable so SET NULL is legal
    PRIMARY KEY (id),
    CONSTRAINT fk_posts_user FOREIGN KEY (user_id) REFERENCES users(id)
        ON DELETE CASCADE ON UPDATE SET NULL
);
`)
	cat := catalog.NewCatalog(db, testutil.DefaultDB)
	tables, err := cat.Tables(ctx)
	require.NoError(t, err)

	posts, ok := tables.GetOk("myschema_test.posts")
	require.True(t, ok, "posts table should be loaded")
	fk, ok := posts.ForeignKeys.GetOk("fk_posts_user")
	require.True(t, ok, "fk_posts_user should be loaded")
	assert.Equal(t, "users", fk.RefTable)
	assert.Equal(t, "myschema_test", fk.RefDB)
	assert.Equal(t, []string{"user_id"}, fk.Columns)
	assert.Equal(t, []string{"id"}, fk.RefCols)
	assert.Equal(t, "CASCADE", fk.OnDelete)
	assert.Equal(t, "SET NULL", fk.OnUpdate)
}

// TestForeignKeyRestrictAndNoActionNormalize confirms the catalog folds
// MySQL's two "no-op" referential actions (RESTRICT and NO ACTION) into
// the empty string, so the diff side compares cleanly with the parser.
func TestForeignKeyRestrictAndNoActionNormalize(t *testing.T) {
	db := testutil.ConnectDB(t)
	ctx := context.Background()
	testutil.SetupDB(t, ctx, db, `
CREATE TABLE users (id BIGINT NOT NULL, PRIMARY KEY (id));
CREATE TABLE posts (
    id BIGINT NOT NULL,
    user_id BIGINT NOT NULL,
    PRIMARY KEY (id),
    CONSTRAINT fk_restrict  FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE RESTRICT,
    CONSTRAINT fk_no_action FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE NO ACTION
);
`)
	cat := catalog.NewCatalog(db, testutil.DefaultDB)
	tables, err := cat.Tables(ctx)
	require.NoError(t, err)
	posts, ok := tables.GetOk("myschema_test.posts")
	require.True(t, ok, "posts table should be loaded")

	r, ok := posts.ForeignKeys.GetOk("fk_restrict")
	require.True(t, ok)
	assert.Empty(t, r.OnDelete, "RESTRICT folded to empty")

	n, ok := posts.ForeignKeys.GetOk("fk_no_action")
	require.True(t, ok)
	assert.Empty(t, n.OnDelete, "NO ACTION folded to empty")
}

// TestCheckConstraintRoundTrip pins loadCheckConstraints' join with
// TABLE_CONSTRAINTS and the `CHECK (<expr>)` definition shape that
// constraintInlineSQL relies on.
func TestCheckConstraintRoundTrip(t *testing.T) {
	db := testutil.ConnectDB(t)
	ctx := context.Background()
	testutil.SetupDB(t, ctx, db, `
CREATE TABLE products (
    id BIGINT NOT NULL,
    price DECIMAL(10,2) NOT NULL,
    PRIMARY KEY (id),
    CONSTRAINT chk_price CHECK (price > 0)
);
`)
	cat := catalog.NewCatalog(db, testutil.DefaultDB)
	tables, err := cat.Tables(ctx)
	require.NoError(t, err)
	products, ok := tables.GetOk("myschema_test.products")
	require.True(t, ok, "products table should be loaded")

	c, ok := products.Constraints.GetOk("chk_price")
	require.True(t, ok)
	assert.Equal(t, model.CheckConstraint, c.Type)
	assert.Contains(t, c.Definition, "CHECK")
	assert.Contains(t, c.Definition, "price")
	assert.True(t, c.Enforced)
}

// TestGeneratedColumnRoundTrip checks STORED and VIRTUAL columns round
// trip with the generation expression intact.
func TestGeneratedColumnRoundTrip(t *testing.T) {
	db := testutil.ConnectDB(t)
	ctx := context.Background()
	testutil.SetupDB(t, ctx, db, `
CREATE TABLE gen (
    id BIGINT NOT NULL,
    a INT NOT NULL,
    b INT NOT NULL,
    sum_stored INT GENERATED ALWAYS AS (a + b) STORED,
    sum_virtual INT GENERATED ALWAYS AS (a + b) VIRTUAL,
    PRIMARY KEY (id)
);
`)
	cat := catalog.NewCatalog(db, testutil.DefaultDB)
	tables, err := cat.Tables(ctx)
	require.NoError(t, err)
	gen, ok := tables.GetOk("myschema_test.gen")
	require.True(t, ok, "gen table should be loaded")

	stored, ok := gen.Columns.GetOk("sum_stored")
	require.True(t, ok, "sum_stored column should be loaded")
	require.NotNil(t, stored.Generated, "Generated must be populated")
	assert.True(t, stored.Stored, "STORED column has Stored=true")

	virtual, ok := gen.Columns.GetOk("sum_virtual")
	require.True(t, ok, "sum_virtual column should be loaded")
	require.NotNil(t, virtual.Generated)
	assert.False(t, virtual.Stored, "VIRTUAL column has Stored=false")
}

// TestIndexCatalogQuirks exercises the STATISTICS branches that were
// previously untested: prefix length, multi-column, and FULLTEXT.
// (DESC index and INVISIBLE index require column hardware that's flaky
// across MySQL versions; covered by the parser-side tests instead.)
func TestIndexCatalogQuirks(t *testing.T) {
	db := testutil.ConnectDB(t)
	ctx := context.Background()
	testutil.SetupDB(t, ctx, db, `
CREATE TABLE quirks (
    id BIGINT NOT NULL,
    a VARCHAR(255) NOT NULL,
    b VARCHAR(64) NOT NULL,
    body TEXT,
    PRIMARY KEY (id),
    KEY idx_a_prefix (a(20)),
    KEY idx_a_b (a, b),
    FULLTEXT KEY ft_body (body)
);
`)
	cat := catalog.NewCatalog(db, testutil.DefaultDB)
	tables, err := cat.Tables(ctx)
	require.NoError(t, err)
	q, ok := tables.GetOk("myschema_test.quirks")
	require.True(t, ok, "quirks table should be loaded")

	prefix, ok := q.Indexes.GetOk("idx_a_prefix")
	require.True(t, ok, "idx_a_prefix should be loaded")
	require.Len(t, prefix.Parts, 1)
	assert.Equal(t, 20, prefix.Parts[0].Length)

	combined, ok := q.Indexes.GetOk("idx_a_b")
	require.True(t, ok, "idx_a_b should be loaded")
	require.Len(t, combined.Parts, 2)
	assert.Equal(t, "a", combined.Parts[0].Column)
	assert.Equal(t, "b", combined.Parts[1].Column)

	ft, ok := q.Indexes.GetOk("ft_body")
	require.True(t, ok, "ft_body should be loaded")
	assert.Equal(t, model.IndexFulltext, ft.KeyType)
}

// TestColumnDefaultNormalisation pins normalizeColumnDefault: catalog
// receives bareword ENUM/temporal defaults from MySQL and wraps them in
// quotes so vitess can re-parse the dump output.
func TestColumnDefaultNormalisation(t *testing.T) {
	db := testutil.ConnectDB(t)
	ctx := context.Background()
	testutil.SetupDB(t, ctx, db, `
CREATE TABLE defs (
    id BIGINT NOT NULL,
    rating ENUM('g','pg','r') NOT NULL DEFAULT 'g',
    quantity INT NOT NULL DEFAULT 0,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (id)
);
`)
	cat := catalog.NewCatalog(db, testutil.DefaultDB)
	tables, err := cat.Tables(ctx)
	require.NoError(t, err)
	defs, ok := tables.GetOk("myschema_test.defs")
	require.True(t, ok, "defs table should be loaded")

	rating, ok := defs.Columns.GetOk("rating")
	require.True(t, ok, "rating column should be loaded")
	require.NotNil(t, rating.Default)
	assert.Equal(t, "'g'", *rating.Default, "ENUM default wrapped in single quotes")

	qty, ok := defs.Columns.GetOk("quantity")
	require.True(t, ok, "quantity column should be loaded")
	require.NotNil(t, qty.Default)
	assert.Equal(t, "0", *qty.Default, "numeric default left bare")

	created, ok := defs.Columns.GetOk("created_at")
	require.True(t, ok, "created_at column should be loaded")
	require.NotNil(t, created.Default)
	assert.Contains(t, *created.Default, "CURRENT_TIMESTAMP", "expression default left bare")
}

// TestColumnDefaultEmptyStringNormalisation pins the type-aware
// normalisation of `DEFAULT ”`. Without it, the catalog reads the
// empty literal back as the bare empty string, parser produces ”
// (two single quotes), and every post-apply plan re-emits MODIFY
// COLUMN. For string-shaped types we normalise to ” on the catalog
// side so the two compare equal.
func TestColumnDefaultEmptyStringNormalisation(t *testing.T) {
	db := testutil.ConnectDB(t)
	ctx := context.Background()
	testutil.SetupDB(t, ctx, db, `
CREATE TABLE empty_defs (
    id BIGINT NOT NULL AUTO_INCREMENT,
    s_var VARCHAR(64) NOT NULL DEFAULT '',
    s_char CHAR(8) NOT NULL DEFAULT '',
    s_vbin VARBINARY(8) NOT NULL DEFAULT '',
    s_enum ENUM('a','b','') NOT NULL DEFAULT '',
    s_set SET('x','y') NOT NULL DEFAULT '',
    PRIMARY KEY (id)
);
`)
	cat := catalog.NewCatalog(db, testutil.DefaultDB)
	tables, err := cat.Tables(ctx)
	require.NoError(t, err)
	defs, ok := tables.GetOk("myschema_test.empty_defs")
	require.True(t, ok)

	for _, c := range []string{"s_var", "s_char", "s_vbin", "s_enum", "s_set"} {
		col, ok := defs.Columns.GetOk(c)
		require.True(t, ok, "%s should be loaded", c)
		require.NotNil(t, col.Default, "%s should have a default", c)
		assert.Equal(t, "''", *col.Default, "%s empty-string default should be quoted", c)
	}
}

// TestColumnDefaultBinaryEmptyStringIsHex pins the documented
// limitation: a fixed-width BINARY column with `DEFAULT ”` does NOT
// round-trip cleanly because MySQL surfaces the empty default as a
// hex literal (`0x` for the degenerate case, `0x000000…` for non-zero
// N), and `columnTypeAllowsEmptyStringDefault` intentionally excludes
// it from the empty-string normalisation. The test exists to (a)
// document the current behaviour and (b) catch a future refactor that
// accidentally normalises the hex literal to ” before the proper
// BINARY-side fix lands. See TODO.md. (VARBINARY does round-trip
// cleanly — its empty default surfaces as the bare empty string, and
// is asserted in TestColumnDefaultEmptyStringNormalisation.)
func TestColumnDefaultBinaryEmptyStringIsHex(t *testing.T) {
	db := testutil.ConnectDB(t)
	ctx := context.Background()
	testutil.SetupDB(t, ctx, db, `
CREATE TABLE bin_defs (
    id BIGINT NOT NULL AUTO_INCREMENT,
    s_bin BINARY(8) NOT NULL DEFAULT '',
    PRIMARY KEY (id)
);
`)
	cat := catalog.NewCatalog(db, testutil.DefaultDB)
	tables, err := cat.Tables(ctx)
	require.NoError(t, err)
	defs, ok := tables.GetOk("myschema_test.bin_defs")
	require.True(t, ok)

	col, ok := defs.Columns.GetOk("s_bin")
	require.True(t, ok)
	require.NotNil(t, col.Default)
	assert.True(t, strings.HasPrefix(*col.Default, "0x"),
		"BINARY empty default should remain a hex literal, got %q", *col.Default)
	assert.NotEqual(t, "''", *col.Default,
		"BINARY must NOT be normalised to '' (round-trip needs separate handling)")
}

// TestViewsRoundTrip is the catalog-side companion to the view fixtures:
// confirms VIEW_DEFINITION + DEFINER + SECURITY are surfaced.
func TestViewsRoundTrip(t *testing.T) {
	db := testutil.ConnectDB(t)
	ctx := context.Background()
	testutil.SetupDB(t, ctx, db, `
CREATE TABLE users (id BIGINT NOT NULL, name VARCHAR(64), PRIMARY KEY (id));
CREATE VIEW active_users AS SELECT id, name FROM users;
`)
	cat := catalog.NewCatalog(db, testutil.DefaultDB)
	views, err := cat.Views(ctx)
	require.NoError(t, err)

	v, ok := views.GetOk("myschema_test.active_users")
	require.True(t, ok)
	assert.Equal(t, "active_users", v.Name)
	assert.Contains(t, v.Definition, "select")
	assert.Contains(t, v.Definition, "users")
	assert.NotEmpty(t, v.Definer, "DEFINER captured even when implicit")
	assert.Equal(t, "DEFINER", v.Security, "SQL SECURITY default")
}

// TestTableMetadata covers ENGINE / table comment / collation surfacing.
// (CHARSET propagation is intentionally NOT compared per-column at the
// diff layer — see TODO.md for the open question.)
func TestTableMetadata(t *testing.T) {
	db := testutil.ConnectDB(t)
	ctx := context.Background()
	testutil.SetupDB(t, ctx, db, `
CREATE TABLE meta (
    id BIGINT NOT NULL,
    PRIMARY KEY (id)
) ENGINE=InnoDB COMMENT='hello world';
`)
	cat := catalog.NewCatalog(db, testutil.DefaultDB)
	tables, err := cat.Tables(ctx)
	require.NoError(t, err)
	m, ok := tables.GetOk("myschema_test.meta")
	require.True(t, ok, "meta table should be loaded")
	require.NotNil(t, m.Engine)
	assert.Equal(t, "InnoDB", *m.Engine)
	require.NotNil(t, m.Comment)
	assert.Equal(t, "hello world", *m.Comment)
}
