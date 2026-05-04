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

// planTestCase is the authoritative shape of a testdata/plan/*.yml fixture.
type planTestCase struct {
	Init            string   `yaml:"init"`                       // SQL to seed the test DB
	Desired         string   `yaml:"desired"`                    // SQL passed to plan
	Plan            string   `yaml:"plan,omitempty"`             // expected plan SQL ("" → expect no diff)
	DisallowedDrops string   `yaml:"disallowed_drops,omitempty"` // expected `-- skipped:` comments (one per line)
	Error           string   `yaml:"error,omitempty"`            // substring expected in plan error (mutually exclusive with Plan)
	AllowDrop       []string `yaml:"allow_drop,omitempty"`
	Include         []string `yaml:"include,omitempty"`
	Exclude         []string `yaml:"exclude,omitempty"`
	AlterAlgorithm  string   `yaml:"alter_algorithm,omitempty"` // appended as ALGORITHM= clause to ALTER TABLE / CREATE INDEX
	AlterLock       string   `yaml:"alter_lock,omitempty"`      // appended as LOCK= clause to ALTER TABLE / CREATE INDEX
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
			AlterOption: myschema.AlterOption{
				AlterAlgorithm: tc.AlterAlgorithm,
				AlterLock:      tc.AlterLock,
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
		assert.Equal(t,
			strings.TrimSpace(tc.DisallowedDrops),
			strings.TrimSpace(r.DisallowedDrops),
			"disallowed-drops mismatch",
		)
	})
}

func TestPlan_BadDSNError(t *testing.T) {
	c := myschema.NewClient(&myschema.Options{DSN: "garbage"})
	_, err := c.Plan(context.Background(), &myschema.PlanOptions{})
	require.Error(t, err)
}

func TestPlan_DSNNonexistentDatabase(t *testing.T) {
	// DSN parses fine but the named database doesn't exist — Database()
	// succeeds (it just splits the DSN), then connect() reaches the
	// driver and the server rejects the access. Plan returns the
	// wrapped connect error.
	c := myschema.NewClient(&myschema.Options{
		DSN: "root@tcp(127.0.0.1:3306)/no_such_db_for_myschema_tests_xyz",
	})
	_, err := c.Plan(context.Background(), &myschema.PlanOptions{})
	require.Error(t, err)
}
