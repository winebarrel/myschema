package myschema_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/winebarrel/myschema"
	"github.com/winebarrel/myschema/internal/testutil"
)

// applyTestCase is the authoritative shape of a testdata/apply/*.yml fixture.
type applyTestCase struct {
	Init           string   `yaml:"init"`              // SQL to seed the test DB
	Desired        string   `yaml:"desired"`           // SQL passed to apply
	Applied        string   `yaml:"applied,omitempty"` // expected SQL written to apply's writer
	AllowDrop      []string `yaml:"allow_drop,omitempty"`
	Include        []string `yaml:"include,omitempty"`
	Exclude        []string `yaml:"exclude,omitempty"`
	AlterAlgorithm string   `yaml:"alter_algorithm,omitempty"` // appended as ALGORITHM= clause to ALTER TABLE / CREATE INDEX
	AlterLock      string   `yaml:"alter_lock,omitempty"`      // appended as LOCK= clause to ALTER TABLE / CREATE INDEX
	VerifyNoDrift  *bool    `yaml:"verify_no_drift,omitempty"` // nil → true; set false to allow follow-up drift
}

func TestApplyYAML(t *testing.T) {
	runYAMLCases(t, "testdata/apply", func(t *testing.T, name string, tc *applyTestCase) {
		ctx := setupCtx()
		conn := testutil.ConnectDB(t)
		testutil.SetupDB(t, ctx, conn, tc.Init)

		desiredFile := writeDesired(t, tc.Desired)
		client := newClient(t)

		var buf bytes.Buffer
		_, err := client.Apply(ctx, &myschema.ApplyOptions{
			Files: []string{desiredFile},
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
		}, &buf)
		require.NoError(t, err)

		assert.Equal(t,
			strings.TrimSpace(tc.Applied),
			strings.TrimSpace(buf.String()),
			"applied SQL mismatch",
		)

		// Drift check: by default re-plan must be empty. Disable per-fixture
		// when a test intentionally leaves residual diff (e.g. suppressed drops).
		verify := true
		if tc.VerifyNoDrift != nil {
			verify = *tc.VerifyNoDrift
		}
		if !verify {
			return
		}
		plan, err := client.Plan(ctx, &myschema.PlanOptions{
			Files: []string{desiredFile},
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
		require.NoError(t, err)
		assert.Empty(t, strings.TrimSpace(plan.SQL), "drift after apply")
	})
}
