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
	assert.Contains(t, got, "CREATE TABLE users (")
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
