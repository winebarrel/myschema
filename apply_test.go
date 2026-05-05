package myschema_test

import (
	"bytes"
	"context"
	"os"
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
	Error          string   `yaml:"error,omitempty"`   // substring expected in apply error (mutually exclusive with Applied / VerifyNoDrift)
	AllowDrop      []string `yaml:"allow_drop,omitempty"`
	Include        []string `yaml:"include,omitempty"`
	Exclude        []string `yaml:"exclude,omitempty"`
	AlterAlgorithm string   `yaml:"alter_algorithm,omitempty"` // appended as ALGORITHM= clause to ALTER TABLE / CREATE INDEX
	AlterLock      string   `yaml:"alter_lock,omitempty"`      // appended as LOCK= clause to ALTER TABLE / CREATE INDEX
	BulkAlter      bool     `yaml:"bulk_alter,omitempty"`      // combine consecutive same-table ALTER TABLE statements
	PreSQL         string   `yaml:"pre_sql,omitempty"`         // SQL run on the connection before the diff (typically session SETs)
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
				BulkAlter:      tc.BulkAlter,
			},
			PreSQLOption: myschema.PreSQLOption{
				PreSQL: tc.PreSQL,
			},
		}, &buf)

		if tc.Error != "" {
			// Enforce the documented mutually-exclusive contract so a
			// malformed fixture (Error set alongside Applied or
			// VerifyNoDrift) can't silently skip the success-path
			// assertions below.
			require.Empty(t, tc.Applied,
				"applyTestCase.Error and Applied are mutually exclusive")
			require.Nil(t, tc.VerifyNoDrift,
				"applyTestCase.Error makes VerifyNoDrift moot — leave it unset")
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.Error)
			return
		}
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
				BulkAlter:      tc.BulkAlter,
			},
		})
		require.NoError(t, err)
		assert.Empty(t, strings.TrimSpace(plan.SQL), "drift after apply")
	})
}

// File-related pre-SQL tests stay in Go (the YAML harness has no
// `pre_sql_file` field — adding one would require fixture-relative
// path handling). Inline-pre-SQL coverage moved to YAML fixtures
// under testdata/apply/pre_sql_*.yml.

// TestApply_PreSQLFile loads pre-SQL from an on-disk file via
// os.ReadFile. stdin (`-`) is rejected upstream by loadPreSQL —
// see TestApply_PreSQLFileStdinRejected for that pin.
func TestApply_PreSQLFile(t *testing.T) {
	ctx := context.Background()
	conn := testutil.ConnectDB(t)
	testutil.SetupDB(t, ctx, conn, "")

	dir := t.TempDir()
	preFile := dir + "/pre.sql"
	require.NoError(t, os.WriteFile(preFile, []byte("SET @myschema_pre_sql_test = 'file';"), 0o600))

	c := newClient(t)
	desiredFile := writeDesired(t, `CREATE TABLE t (id INT NOT NULL, PRIMARY KEY (id));`)
	var buf bytes.Buffer
	_, err := c.Apply(ctx, &myschema.ApplyOptions{
		Files:        []string{desiredFile},
		PreSQLOption: myschema.PreSQLOption{PreSQLFile: preFile},
	}, &buf)
	require.NoError(t, err)
}

// TestApply_PreSQLBothSetError pins the runtime mutually-exclusive
// guard. (kong's `xor:"pre-sql"` tag rejects this at parse time for
// CLI invocations; the runtime check stays in place to catch
// programmatic API callers that build options structs directly.)
func TestApply_PreSQLBothSetError(t *testing.T) {
	c := newClient(t)
	desiredFile := writeDesired(t, `CREATE TABLE t (id INT NOT NULL, PRIMARY KEY (id));`)
	var buf bytes.Buffer
	_, err := c.Apply(context.Background(), &myschema.ApplyOptions{
		Files: []string{desiredFile},
		PreSQLOption: myschema.PreSQLOption{
			PreSQL:     "SET @x = 1;",
			PreSQLFile: "/tmp/whatever.sql",
		},
	}, &buf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mutually exclusive")
}

// TestApply_PreSQLFileMissing pins the file-read error path in
// loadPreSQL (covered the "happy" --pre-sql-file path above; this
// one drives the bad-path branch).
func TestApply_PreSQLFileMissing(t *testing.T) {
	c := newClient(t)
	desiredFile := writeDesired(t, `CREATE TABLE t (id INT NOT NULL, PRIMARY KEY (id));`)
	var buf bytes.Buffer
	_, err := c.Apply(context.Background(), &myschema.ApplyOptions{
		Files:        []string{desiredFile},
		PreSQLOption: myschema.PreSQLOption{PreSQLFile: "/nonexistent/path/to/pre.sql"},
	}, &buf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pre-sql")
	assert.Contains(t, err.Error(), "/nonexistent/path/to/pre.sql")
}

// TestApply_PreSQLEmptyFile: a pre-SQL file that exists but is
// empty (or whitespace-only) must round-trip to a no-op rather
// than fail the parser split.
func TestApply_PreSQLEmptyFile(t *testing.T) {
	ctx := context.Background()
	conn := testutil.ConnectDB(t)
	testutil.SetupDB(t, ctx, conn, "")

	dir := t.TempDir()
	preFile := dir + "/empty.sql"
	require.NoError(t, os.WriteFile(preFile, []byte("   \n  \n"), 0o600))

	c := newClient(t)
	desiredFile := writeDesired(t, `CREATE TABLE t (id INT NOT NULL, PRIMARY KEY (id));`)
	var buf bytes.Buffer
	_, err := c.Apply(ctx, &myschema.ApplyOptions{
		Files:        []string{desiredFile},
		PreSQLOption: myschema.PreSQLOption{PreSQLFile: preFile},
	}, &buf)
	require.NoError(t, err)
}

// TestApply_PreSQLFileStdinRejected pins the outright rejection of
// `--pre-sql-file=-` (stdin). The desired-SQL file args already
// accept `-`; allowing it for pre-SQL too would let both inputs
// fight over stdin (the second read would hit EOF and silently
// truncate). Pre-SQL is small enough that --pre-sql / env covers
// the no-real-file case, so on-disk paths are the only file shape
// supported.
func TestApply_PreSQLFileStdinRejected(t *testing.T) {
	c := newClient(t)
	_, err := c.Apply(context.Background(), &myschema.ApplyOptions{
		Files:        []string{writeDesired(t, `CREATE TABLE t (id INT NOT NULL, PRIMARY KEY (id));`)},
		PreSQLOption: myschema.PreSQLOption{PreSQLFile: "-"},
	}, &bytes.Buffer{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stdin")
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
	//
	// Built off MYSCHEMA_TEST_DSN so the MySQL 9.x CI leg (port 3307)
	// hits the right server.
	c := newClientWithDB(t, "no_such_db_for_myschema_tests_xyz")
	_, err := c.Apply(context.Background(), &myschema.ApplyOptions{}, nil)
	require.Error(t, err)
}
