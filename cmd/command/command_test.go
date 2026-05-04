package command_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/winebarrel/myschema"
	"github.com/winebarrel/myschema/cmd/command"
	"github.com/winebarrel/myschema/internal/testutil"
)

// newTestClient mirrors test_helper_test.go's newClient — the Client has to
// be constructed from Options because that's what carries the DSN. We
// duplicate the wiring here (rather than export a helper) because cmd/command
// sits below the root package.
func newTestClient(t *testing.T) *myschema.Client {
	t.Helper()
	base := os.Getenv("MYSCHEMA_TEST_DSN")
	if base == "" {
		base = "root@tcp(127.0.0.1:3306)/"
	}
	if !strings.HasSuffix(base, "/") {
		base += "/"
	}
	return myschema.NewClient(&myschema.Options{
		DSN: base + testutil.DefaultDB,
	})
}

func writeDesiredFile(t *testing.T, sql string) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "desired.sql")
	require.NoError(t, os.WriteFile(f, []byte(sql), 0o644))
	return f
}

func TestPlan_Run(t *testing.T) {
	ctx := context.Background()
	conn := testutil.ConnectDB(t)
	testutil.SetupDB(t, ctx, conn, "")

	desired := writeDesiredFile(t, `CREATE TABLE users (
    id BIGINT NOT NULL,
    PRIMARY KEY (id)
);`)
	client := newTestClient(t)

	var buf bytes.Buffer
	cmd := &command.Plan{PlanOptions: myschema.PlanOptions{
		Files:      []string{desired},
		DropPolicy: myschema.DropPolicy{AllowDrop: []string{"all"}},
	}}
	require.NoError(t, cmd.Run(ctx, client, &buf))
	got := buf.String()
	assert.Contains(t, got, "-- Plan for database "+testutil.DefaultDB)
	assert.Contains(t, got, "CREATE TABLE "+testutil.DefaultDB+".users")
}

func TestPlan_Run_NoChanges(t *testing.T) {
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
	cmd := &command.Plan{PlanOptions: myschema.PlanOptions{
		Files: []string{desired},
	}}
	require.NoError(t, cmd.Run(ctx, client, &buf))
	got := buf.String()
	assert.Contains(t, got, "-- No changes")
}

func TestPlan_Run_DropDeniedPrintsSkippedComment(t *testing.T) {
	// When the only diff is a DROP and --allow-drop is unset, plan emits
	// the DROP as a `-- skipped: …` line and still reports "-- No changes"
	// because no executable DDL is generated.
	ctx := context.Background()
	conn := testutil.ConnectDB(t)
	testutil.SetupDB(t, ctx, conn, `CREATE TABLE users (
    id BIGINT NOT NULL,
    PRIMARY KEY (id)
);`)

	desired := writeDesiredFile(t, "") // wipe everything
	client := newTestClient(t)

	var buf bytes.Buffer
	cmd := &command.Plan{PlanOptions: myschema.PlanOptions{
		Files: []string{desired},
	}}
	require.NoError(t, cmd.Run(ctx, client, &buf))
	got := buf.String()
	assert.Contains(t, got, "-- skipped: DROP TABLE "+testutil.DefaultDB+".users;")
	assert.Contains(t, got, "-- No changes")
}

func TestPlan_Run_BadDSNError(t *testing.T) {
	// Surface the error path through Plan.Run.
	desired := writeDesiredFile(t, "CREATE TABLE t (id INT);")
	client := myschema.NewClient(&myschema.Options{DSN: "no_db_in_dsn"})

	var buf bytes.Buffer
	cmd := &command.Plan{PlanOptions: myschema.PlanOptions{Files: []string{desired}}}
	err := cmd.Run(context.Background(), client, &buf)
	require.Error(t, err)
}

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

func TestDump_Run(t *testing.T) {
	ctx := context.Background()
	conn := testutil.ConnectDB(t)
	testutil.SetupDB(t, ctx, conn, `CREATE TABLE users (
    id BIGINT NOT NULL,
    PRIMARY KEY (id)
);`)
	client := newTestClient(t)

	var buf bytes.Buffer
	cmd := &command.Dump{}
	require.NoError(t, cmd.Run(ctx, client, &buf))
	got := buf.String()
	assert.Contains(t, got, "-- Dump of database "+testutil.DefaultDB)
	assert.Contains(t, got, "CREATE TABLE "+testutil.DefaultDB+".users")
}

func TestDump_Run_EmptyDatabase(t *testing.T) {
	ctx := context.Background()
	conn := testutil.ConnectDB(t)
	testutil.SetupDB(t, ctx, conn, "")
	client := newTestClient(t)

	var buf bytes.Buffer
	cmd := &command.Dump{}
	require.NoError(t, cmd.Run(ctx, client, &buf))
	got := buf.String()
	assert.Contains(t, got, "-- Dump of database "+testutil.DefaultDB)
	assert.Contains(t, got, "0 table(s)")
}

func TestDump_Run_BadDSNError(t *testing.T) {
	client := myschema.NewClient(&myschema.Options{DSN: "no_db_in_dsn"})

	var buf bytes.Buffer
	cmd := &command.Dump{}
	err := cmd.Run(context.Background(), client, &buf)
	require.Error(t, err)
}
