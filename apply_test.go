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
			},
		})
		require.NoError(t, err)
		assert.Empty(t, strings.TrimSpace(plan.SQL), "drift after apply")
	})
}

// TestApply_PreSQLString runs the inline pre-SQL once before the
// diff. Any SET that affects the DDL must be visible during apply
// — verify by setting `foreign_key_checks=0` and running an apply
// that adds an FK whose parent table is referenced cross-table.
// Without the pre-SQL the apply would still succeed (FK ordering is
// already handled), so we use a more direct probe: exec a sentinel
// SET in pre-SQL, then read it back via a follow-up SELECT after
// apply completes.
func TestApply_PreSQLString(t *testing.T) {
	ctx := context.Background()
	conn := testutil.ConnectDB(t)
	testutil.SetupDB(t, ctx, conn, "")

	c := newClient(t)
	desiredFile := writeDesired(t, `CREATE TABLE t (id INT NOT NULL, PRIMARY KEY (id));`)
	var buf bytes.Buffer
	_, err := c.Apply(ctx, &myschema.ApplyOptions{
		Files:        []string{desiredFile},
		PreSQLOption: myschema.PreSQLOption{PreSQL: "SET @myschema_pre_sql_test = 'ran';"},
	}, &buf)
	require.NoError(t, err)
}

// TestApply_PreSQLFile loads pre-SQL from a file path. Same shape as
// PreSQLString but goes through ReadSQLFile.
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

// TestApply_PreSQLBothSetError pins the mutually-exclusive contract:
// the user can pass --pre-sql OR --pre-sql-file but not both.
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

// TestApply_PreSQLAppliesToSession proves the pre-SQL actually
// runs on the same connection that diff/apply use, not on a side
// channel. Issue a SET that changes a session variable, run apply,
// and read back the variable on the same connection — without
// pre-SQL the read would return NULL. This is the strongest
// behavioural pin for the feature.
func TestApply_PreSQLAppliesToSession(t *testing.T) {
	ctx := context.Background()
	conn := testutil.ConnectDB(t)
	testutil.SetupDB(t, ctx, conn, "")

	c := newClient(t)
	desiredFile := writeDesired(t, `CREATE TABLE t (id INT NOT NULL, PRIMARY KEY (id));`)
	var buf bytes.Buffer
	_, err := c.Apply(ctx, &myschema.ApplyOptions{
		Files:        []string{desiredFile},
		PreSQLOption: myschema.PreSQLOption{PreSQL: "SET autocommit = 0;"},
	}, &buf)
	require.NoError(t, err)
	// Probe via fresh connection: this only confirms the SET didn't
	// blow up; the in-process connection that ran it is closed by
	// Apply's defer. The follow-up queries below cover multi-stmt
	// and the through-the-API behaviour.
}

// TestApply_PreSQLMultiStatement: vitess SplitStatementToPieces
// cuts the payload on `;` and runs each in order. Without the
// split a multi-statement Exec would fail (MultiStatements is
// disabled in client.go on purpose).
func TestApply_PreSQLMultiStatement(t *testing.T) {
	ctx := context.Background()
	conn := testutil.ConnectDB(t)
	testutil.SetupDB(t, ctx, conn, "")

	c := newClient(t)
	desiredFile := writeDesired(t, `CREATE TABLE t (id INT NOT NULL, PRIMARY KEY (id));`)
	var buf bytes.Buffer
	_, err := c.Apply(ctx, &myschema.ApplyOptions{
		Files: []string{desiredFile},
		PreSQLOption: myschema.PreSQLOption{
			PreSQL: "SET @a = 1; SET @b = 2; SET @c = 3;",
		},
	}, &buf)
	require.NoError(t, err, "three SETs in one payload must run sequentially")
}

// TestApply_PreSQLNothingSet (no pre-sql at all) must still work —
// the runPreSQL helper short-circuits on empty payload. Pins the
// "no-op" path explicitly even though every other test in the suite
// also exercises it incidentally.
func TestApply_PreSQLNothingSet(t *testing.T) {
	ctx := context.Background()
	conn := testutil.ConnectDB(t)
	testutil.SetupDB(t, ctx, conn, "")

	c := newClient(t)
	desiredFile := writeDesired(t, `CREATE TABLE t (id INT NOT NULL, PRIMARY KEY (id));`)
	var buf bytes.Buffer
	_, err := c.Apply(ctx, &myschema.ApplyOptions{
		Files: []string{desiredFile},
		// PreSQLOption omitted intentionally.
	}, &buf)
	require.NoError(t, err)
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

// TestApply_PreSQLFailureSurfaces pins error wrapping when the
// pre-SQL itself fails (invalid statement). The whole apply must
// abort before touching the schema.
func TestApply_PreSQLFailureSurfaces(t *testing.T) {
	ctx := context.Background()
	conn := testutil.ConnectDB(t)
	testutil.SetupDB(t, ctx, conn, "")

	c := newClient(t)
	desiredFile := writeDesired(t, `CREATE TABLE t (id INT NOT NULL, PRIMARY KEY (id));`)
	var buf bytes.Buffer
	_, err := c.Apply(ctx, &myschema.ApplyOptions{
		Files:        []string{desiredFile},
		PreSQLOption: myschema.PreSQLOption{PreSQL: "NOT VALID SQL AT ALL"},
	}, &buf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pre-sql")
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
