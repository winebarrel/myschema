package catalog_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/winebarrel/myschema/catalog"
	"github.com/winebarrel/myschema/internal/testutil"
)

// TestTablesRoundTrip checks that a table created via raw SQL is read back by
// the catalog with the right shape. Requires a reachable MySQL (the test
// fails if not — start it with `docker compose up -d`).
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

	cat := catalog.NewCatalog(db, []string{testutil.DefaultDB})
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
}
