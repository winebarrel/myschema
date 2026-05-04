package myschema_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/winebarrel/myschema"
	"github.com/winebarrel/myschema/internal/testutil"
)

// dumpTestCase is the authoritative shape of a testdata/dump/*.yml fixture.
type dumpTestCase struct {
	Init    string   `yaml:"init"`              // SQL to seed the test DB
	Dump    string   `yaml:"dump"`              // expected dump body (header line is stripped before compare)
	Include []string `yaml:"include,omitempty"` // pattern(s) for FilterOptions.Include
	Exclude []string `yaml:"exclude,omitempty"` // pattern(s) for FilterOptions.Exclude
}

func TestDumpYAML(t *testing.T) {
	runYAMLCases(t, "testdata/dump", func(t *testing.T, name string, tc *dumpTestCase) {
		ctx := setupCtx()
		conn := testutil.ConnectDB(t)
		testutil.SetupDB(t, ctx, conn, tc.Init)

		client := newClient(t)
		r, err := client.Dump(ctx, &myschema.DumpOptions{
			FilterOptions: myschema.FilterOptions{
				Include: tc.Include,
				Exclude: tc.Exclude,
			},
		})
		require.NoError(t, err)
		assert.Equal(t,
			strings.TrimSpace(tc.Dump),
			strings.TrimSpace(r.SQL),
			"dump output mismatch",
		)
	})
}

func TestDumpResult_String(t *testing.T) {
	// DumpResult satisfies fmt.Stringer (so callers can write it
	// straight to a Writer). Pin the contract: String() returns the
	// SQL field verbatim.
	r := &myschema.DumpResult{SQL: "CREATE TABLE x (id INT);"}
	assert.Equal(t, "CREATE TABLE x (id INT);", r.String())
}

func TestDump_BadDSNError(t *testing.T) {
	c := myschema.NewClient(&myschema.Options{DSN: "garbage"})
	_, err := c.Dump(context.Background(), &myschema.DumpOptions{})
	require.Error(t, err)
}

func TestDump_DSNNonexistentDatabase(t *testing.T) {
	// DSN parses fine but the named database doesn't exist — Database()
	// succeeds (DSN-name split), then connect() reaches the driver and
	// the server rejects the access. Dump returns the wrapped error.
	//
	// Built off MYSCHEMA_TEST_DSN so the MySQL 9.x CI leg (port 3307)
	// hits the right server.
	c := newClientWithDB(t, "no_such_db_for_myschema_tests_xyz")
	_, err := c.Dump(context.Background(), &myschema.DumpOptions{})
	require.Error(t, err)
}
