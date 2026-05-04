package catalog_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/winebarrel/myschema/catalog"
	"github.com/winebarrel/myschema/internal/testutil"
	"github.com/winebarrel/myschema/model"
)

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
		assert.Nil(t, catalog.NullIfMatchesTableDefault(nil, new("utf8mb4")))
	})
	t.Run("table default nil passes col through", func(t *testing.T) {
		got := catalog.NullIfMatchesTableDefault(new("utf8mb4"), nil)
		assert.Equal(t, "utf8mb4", *got)
	})
	t.Run("matching collapses to nil", func(t *testing.T) {
		assert.Nil(t, catalog.NullIfMatchesTableDefault(new("utf8mb4"), new("utf8mb4")))
	})
	t.Run("differing passes col through", func(t *testing.T) {
		got := catalog.NullIfMatchesTableDefault(new("latin1"), new("utf8mb4"))
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

// All tests below require a reachable MySQL (start it with
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
	assert.True(t, c.Enforced, "default CHECK is enforced")
}

// TestCheckConstraintNotEnforcedRoundTrip pins loadCheckConstraints
// reading `tc.ENFORCED` from information_schema.TABLE_CONSTRAINTS.
// Pre-fix the field was hard-coded `true`, causing every NOT
// ENFORCED check to drift on every plan. Catalog must report
// Enforced=false when MySQL stored ENFORCED='NO'.
func TestCheckConstraintNotEnforcedRoundTrip(t *testing.T) {
	db := testutil.ConnectDB(t)
	ctx := context.Background()
	testutil.SetupDB(t, ctx, db, `
CREATE TABLE products (
    id BIGINT NOT NULL,
    price DECIMAL(10,2) NOT NULL,
    PRIMARY KEY (id),
    CONSTRAINT chk_price CHECK (price > 0) NOT ENFORCED
);
`)
	cat := catalog.NewCatalog(db, testutil.DefaultDB)
	tables, err := cat.Tables(ctx)
	require.NoError(t, err)
	products, ok := tables.GetOk("myschema_test.products")
	require.True(t, ok)

	c, ok := products.Constraints.GetOk("chk_price")
	require.True(t, ok)
	assert.False(t, c.Enforced, "NOT ENFORCED must round-trip from tc.ENFORCED='NO'")
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
// normalisation of the empty-string column default. Without it the
// catalog reads back the bare empty string while the parser produces
// the quoted empty literal, and every post-apply plan re-emits
// MODIFY COLUMN. For string-shaped types we wrap the bare empty
// string on the catalog side so the two sides compare equal.
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

// TestColumnDefaultBinaryEmptyStringNormalisation pins the catalog-side
// normalisation of fixed-width BINARY(N) with an empty default. MySQL
// surfaces it through information_schema.COLUMNS.COLUMN_DEFAULT as the
// literal two-character string "0x" — independent of N, so all of
// BINARY(1) / BINARY(4) / BINARY(16) come back the same. The parser
// side stores the quoted empty literal, so without normalisation every
// post-apply plan re-emits MODIFY COLUMN. The catalog rewrites the
// "0x" sentinel for BINARY-prefix types to match the parser side.
func TestColumnDefaultBinaryEmptyStringNormalisation(t *testing.T) {
	db := testutil.ConnectDB(t)
	ctx := context.Background()
	testutil.SetupDB(t, ctx, db, `
CREATE TABLE bin_defs (
    id BIGINT NOT NULL AUTO_INCREMENT,
    s_bin1 BINARY(1) NOT NULL DEFAULT '',
    s_bin4 BINARY(4) NOT NULL DEFAULT '',
    s_bin16 BINARY(16) NOT NULL DEFAULT '',
    PRIMARY KEY (id)
);
`)
	cat := catalog.NewCatalog(db, testutil.DefaultDB)
	tables, err := cat.Tables(ctx)
	require.NoError(t, err)
	defs, ok := tables.GetOk("myschema_test.bin_defs")
	require.True(t, ok)

	for _, c := range []string{"s_bin1", "s_bin4", "s_bin16"} {
		col, ok := defs.Columns.GetOk(c)
		require.True(t, ok, "%s should be loaded", c)
		require.NotNil(t, col.Default, "%s should have a default", c)
		assert.Equal(t, "''", *col.Default,
			"%s empty BINARY default should be normalised to ''", c)
	}
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
