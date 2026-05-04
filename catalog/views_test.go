package catalog_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/winebarrel/myschema/catalog"
	"github.com/winebarrel/myschema/internal/testutil"
)

// TestViewsRoundTrip pins the catalog-side view loader: VIEW_DEFINITION
// + CHECK_OPTION are surfaced via information_schema.VIEWS. DEFINER /
// SQL SECURITY are intentionally not selected (out of scope for v1 —
// see CAVEATS.md).
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
}
