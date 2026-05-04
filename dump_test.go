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

func TestDump_BadDSNError(t *testing.T) {
	c := myschema.NewClient(&myschema.Options{DSN: "garbage"})
	_, err := c.Dump(context.Background(), &myschema.DumpOptions{})
	require.Error(t, err)
}
