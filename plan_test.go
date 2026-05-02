package myschema_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/winebarrel/myschema"
	"github.com/winebarrel/myschema/internal/testutil"
)

// planTestCase is the authoritative shape of a testdata/plan/*.yml fixture.
type planTestCase struct {
	Init      string   `yaml:"init"`            // SQL to seed the test DB
	Desired   string   `yaml:"desired"`         // SQL passed to plan
	Plan      string   `yaml:"plan,omitempty"`  // expected plan SQL ("" → expect no diff)
	Error     string   `yaml:"error,omitempty"` // substring expected in plan error (mutually exclusive with Plan)
	AllowDrop []string `yaml:"allow_drop,omitempty"`
	Include   []string `yaml:"include,omitempty"`
	Exclude   []string `yaml:"exclude,omitempty"`
}

func TestPlanYAML(t *testing.T) {
	runYAMLCases(t, "testdata/plan", func(t *testing.T, name string, tc *planTestCase) {
		ctx := setupCtx()
		conn := testutil.ConnectDB(t)
		testutil.SetupDB(t, ctx, conn, tc.Init)

		client := newClient(t)
		r, err := client.Plan(ctx, &myschema.PlanOptions{
			Files: []string{writeDesired(t, tc.Desired)},
			DropPolicy: myschema.DropPolicy{
				AllowDrop: tc.AllowDrop,
			},
			FilterOptions: myschema.FilterOptions{
				Include: tc.Include,
				Exclude: tc.Exclude,
			},
		})

		if tc.Error != "" {
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.Error)
			return
		}

		require.NoError(t, err)
		assert.Equal(t,
			strings.TrimSpace(tc.Plan),
			strings.TrimSpace(r.SQL),
			"plan output mismatch",
		)
	})
}
