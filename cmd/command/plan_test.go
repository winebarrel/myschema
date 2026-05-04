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
