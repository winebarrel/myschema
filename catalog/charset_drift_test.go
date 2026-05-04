package catalog_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/winebarrel/myschema/internal/testutil"
	"github.com/winebarrel/myschema/model"
)

// TestCharsetDefaultCollationsCoverServer pins that
// `model.defaultCollations` (the parser-side hardcoded charset →
// default-collation table) covers every charset the live server
// reports in `information_schema.CHARACTER_SETS`. If MySQL ever ships
// a new charset (or the project bumps the baseline), this test fails
// loudly so the map can be filled in before drift starts.
//
// Lives under catalog/ rather than model/ because it requires a
// reachable MySQL — model/* tests are pure-unit so `make test-unit`
// (no-MySQL) stays runnable.
func TestCharsetDefaultCollationsCoverServer(t *testing.T) {
	db := testutil.ConnectDB(t)
	ctx := context.Background()
	rows, err := db.QueryContext(ctx, `
SELECT CHARACTER_SET_NAME, DEFAULT_COLLATE_NAME
FROM   information_schema.CHARACTER_SETS`)
	require.NoError(t, err)
	defer rows.Close() //nolint:errcheck
	for rows.Next() {
		var charset, want string
		require.NoError(t, rows.Scan(&charset, &want))
		got := model.DefaultCollationOf(charset)
		assert.Equal(t, want, got,
			"model.defaultCollations missing or wrong for charset %q", charset)
	}
	require.NoError(t, rows.Err())
}
