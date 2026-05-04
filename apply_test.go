package myschema_test

import (
	"bytes"
	"context"
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

func TestApply_BadDSNError(t *testing.T) {
	// Pins the apply.go error-wrap path through Database() failing on a
	// malformed DSN.
	c := myschema.NewClient(&myschema.Options{DSN: "garbage"})
	_, err := c.Apply(context.Background(), &myschema.ApplyOptions{}, nil)
	require.Error(t, err)
}

func TestApply_DSNNonexistentDatabase(t *testing.T) {
	// Pins the apply.go connect() error path: DSN parses fine but the
	// referenced database doesn't exist, so the underlying driver
	// rejects the connection at first use. Surfaces from connect() →
	// Apply returns the wrapped error.
	c := myschema.NewClient(&myschema.Options{
		DSN: "root@tcp(127.0.0.1:3306)/no_such_db_for_myschema_tests_xyz",
	})
	_, err := c.Apply(context.Background(), &myschema.ApplyOptions{}, nil)
	require.Error(t, err)
}

func TestApply_ExecContextErrorWrapped(t *testing.T) {
	// Pins apply.go:54-56 — when MySQL rejects a generated DDL, Apply
	// wraps the error with `execute %q: %w`. Setup: a table with
	// duplicate values; desired adds a UNIQUE index on the duplicated
	// column. The diff emits a valid ADD UNIQUE, which MySQL rejects
	// (Error 1062 duplicate entry).
	ctx := context.Background()
	conn := testutil.ConnectDB(t)
	testutil.SetupDB(t, ctx, conn, `
CREATE TABLE t (
    id INT NOT NULL,
    name VARCHAR(10),
    PRIMARY KEY (id)
);
INSERT INTO t (id, name) VALUES (1, 'dup'), (2, 'dup');`)

	desired := writeDesired(t, `
CREATE TABLE t (
    id INT NOT NULL,
    name VARCHAR(10),
    PRIMARY KEY (id),
    UNIQUE KEY uq_name (name)
);`)

	c := newClient(t)
	var buf bytes.Buffer
	_, err := c.Apply(ctx, &myschema.ApplyOptions{Files: []string{desired}}, &buf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "execute ")
}
