package command_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/winebarrel/myschema"
	"github.com/winebarrel/myschema/cmd/command"
	"github.com/winebarrel/myschema/internal/testutil"
)

func TestApply_Run(t *testing.T) {
	ctx := context.Background()
	conn := testutil.ConnectDB(t)
	testutil.SetupDB(t, ctx, conn, "")

	desired := writeDesiredFile(t, `CREATE TABLE users (
    id BIGINT NOT NULL,
    PRIMARY KEY (id)
);`)
	client := newTestClient(t)

	var buf bytes.Buffer
	cmd := &command.Apply{ApplyOptions: myschema.ApplyOptions{
		Files:      []string{desired},
		DropPolicy: myschema.DropPolicy{AllowDrop: []string{"all"}},
	}}
	require.NoError(t, cmd.Run(ctx, client, &buf))
	got := buf.String()
	assert.Contains(t, got, "-- Apply to database "+testutil.DefaultDB)
	assert.Contains(t, got, "CREATE TABLE "+testutil.DefaultDB+".users")

	// Sanity-check that the table actually exists in the test DB.
	var n int
	require.NoError(t, conn.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM information_schema.TABLES
		   WHERE TABLE_SCHEMA = ? AND TABLE_NAME = 'users'`,
		testutil.DefaultDB,
	).Scan(&n))
	assert.Equal(t, 1, n)
}

func TestApply_Run_NoChanges(t *testing.T) {
	ctx := context.Background()
	conn := testutil.ConnectDB(t)
	initSQL := `CREATE TABLE users (
    id BIGINT NOT NULL,
    PRIMARY KEY (id)
);`
	testutil.SetupDB(t, ctx, conn, initSQL)

	desired := writeDesiredFile(t, initSQL)
	client := newTestClient(t)

	var buf bytes.Buffer
	cmd := &command.Apply{ApplyOptions: myschema.ApplyOptions{
		Files: []string{desired},
	}}
	require.NoError(t, cmd.Run(ctx, client, &buf))
	got := buf.String()
	assert.Contains(t, got, "-- No changes")
}

func TestApply_Run_DropDeniedShowsSkippedAndNoChanges(t *testing.T) {
	// Apply mirror of the plan drop-denied case. The DROP is suppressed,
	// no DDL runs, and the suppressed-drop comment + "-- No changes" both
	// appear in the output. The table must still exist afterwards.
	ctx := context.Background()
	conn := testutil.ConnectDB(t)
	testutil.SetupDB(t, ctx, conn, `CREATE TABLE users (
    id BIGINT NOT NULL,
    PRIMARY KEY (id)
);`)

	desired := writeDesiredFile(t, "")
	client := newTestClient(t)

	var buf bytes.Buffer
	cmd := &command.Apply{ApplyOptions: myschema.ApplyOptions{
		Files: []string{desired},
	}}
	require.NoError(t, cmd.Run(ctx, client, &buf))
	got := buf.String()
	assert.Contains(t, got, "-- skipped: DROP TABLE "+testutil.DefaultDB+".users;")
	assert.Contains(t, got, "-- No changes")

	var n int
	require.NoError(t, conn.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM information_schema.TABLES
		   WHERE TABLE_SCHEMA = ? AND TABLE_NAME = 'users'`,
		testutil.DefaultDB,
	).Scan(&n))
	assert.Equal(t, 1, n, "drop must have been suppressed")
}

func TestApply_Run_BadDSNError(t *testing.T) {
	desired := writeDesiredFile(t, "CREATE TABLE t (id INT);")
	client := myschema.NewClient(&myschema.Options{DSN: "no_db_in_dsn"})

	var buf bytes.Buffer
	cmd := &command.Apply{ApplyOptions: myschema.ApplyOptions{
		Files:      []string{desired},
		DropPolicy: myschema.DropPolicy{AllowDrop: []string{"all"}},
	}}
	err := cmd.Run(context.Background(), client, &buf)
	require.Error(t, err)
}
