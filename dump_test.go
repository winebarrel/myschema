package myschema_test

import (
	"context"
	"os"
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

// TestDump_SplitTablesAndViews pins the per-object file output for
// `--split=<dir>`. Two tables + one view → three files in the dir,
// each containing the same SQL TableToSQL / ViewToSQL would emit
// in concat mode. DumpResult.SQL stays empty so the cmd layer
// knows not to also dump the SQL body to stdout.
func TestDump_SplitTablesAndViews(t *testing.T) {
	ctx := context.Background()
	conn := testutil.ConnectDB(t)
	testutil.SetupDB(t, ctx, conn, `
CREATE TABLE users (id INT NOT NULL, PRIMARY KEY (id));
CREATE TABLE posts (id INT NOT NULL, PRIMARY KEY (id));
CREATE VIEW v_users AS SELECT id FROM users;
`)

	dir := t.TempDir() + "/out"
	c := newClient(t)
	r, err := c.Dump(ctx, &myschema.DumpOptions{
		SplitDir: dir,
	})
	require.NoError(t, err)
	// SQL stays empty in split mode — cmd layer relies on this to
	// skip writing the SQL body to stdout.
	assert.Empty(t, r.SQL)
	// Counts populate as in concat mode.
	assert.Equal(t, 2, r.Count.Tables)
	assert.Equal(t, 1, r.Count.Views)

	for _, name := range []string{"users.sql", "posts.sql", "v_users.sql"} {
		body, rerr := os.ReadFile(dir + "/" + name)
		require.NoError(t, rerr, "%s should exist", name)
		assert.NotEmpty(t, body, "%s should be non-empty", name)
	}

	// Spot-check content shape so a future refactor can't quietly
	// switch from TableToSQL / ViewToSQL to a different emitter.
	users, err := os.ReadFile(dir + "/users.sql")
	require.NoError(t, err)
	assert.Contains(t, string(users), "CREATE TABLE users (")

	view, err := os.ReadFile(dir + "/v_users.sql")
	require.NoError(t, err)
	assert.Contains(t, string(view), "CREATE OR REPLACE VIEW v_users")
}

// TestDump_SplitCreatesDir confirms the destination is mkdir-p'd
// when missing — the user shouldn't have to pre-create the dir.
func TestDump_SplitCreatesDir(t *testing.T) {
	ctx := context.Background()
	conn := testutil.ConnectDB(t)
	testutil.SetupDB(t, ctx, conn, `CREATE TABLE t (id INT NOT NULL, PRIMARY KEY (id));`)

	// Nested path under TempDir, neither directory exists yet.
	dir := t.TempDir() + "/a/b/c"
	c := newClient(t)
	_, err := c.Dump(ctx, &myschema.DumpOptions{SplitDir: dir})
	require.NoError(t, err)
	body, err := os.ReadFile(dir + "/t.sql")
	require.NoError(t, err)
	assert.Contains(t, string(body), "CREATE TABLE t (")
}

// TestDump_SplitOverwritesExistingFiles: re-running dump must
// rewrite per-object files in place (idempotent). Stale files
// from previous runs are NOT cleaned up — that's the operator's
// responsibility.
func TestDump_SplitOverwritesExistingFiles(t *testing.T) {
	ctx := context.Background()
	conn := testutil.ConnectDB(t)
	testutil.SetupDB(t, ctx, conn, `CREATE TABLE t (id INT NOT NULL, PRIMARY KEY (id));`)

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(dir+"/t.sql", []byte("STALE"), 0o600))
	require.NoError(t, os.WriteFile(dir+"/orphan.sql", []byte("LEFT BEHIND"), 0o600))

	c := newClient(t)
	_, err := c.Dump(ctx, &myschema.DumpOptions{SplitDir: dir})
	require.NoError(t, err)

	// t.sql overwritten with fresh content.
	got, err := os.ReadFile(dir + "/t.sql")
	require.NoError(t, err)
	assert.Contains(t, string(got), "CREATE TABLE t (")
	assert.NotContains(t, string(got), "STALE")

	// orphan.sql is left alone — split is mkdir-and-write, not
	// rsync-style sync. Documented as operator responsibility.
	orphan, err := os.ReadFile(dir + "/orphan.sql")
	require.NoError(t, err)
	assert.Equal(t, "LEFT BEHIND", string(orphan))
}

// TestDump_SplitRespectsFilters: --include / --exclude pre-filter
// the table set the same way concat mode does, so split only emits
// files for objects that pass the filter.
func TestDump_SplitRespectsFilters(t *testing.T) {
	ctx := context.Background()
	conn := testutil.ConnectDB(t)
	testutil.SetupDB(t, ctx, conn, `
CREATE TABLE users (id INT NOT NULL, PRIMARY KEY (id));
CREATE TABLE posts (id INT NOT NULL, PRIMARY KEY (id));
CREATE TABLE legacy (id INT NOT NULL, PRIMARY KEY (id));
`)

	dir := t.TempDir()
	c := newClient(t)
	_, err := c.Dump(ctx, &myschema.DumpOptions{
		SplitDir:      dir,
		FilterOptions: myschema.FilterOptions{Exclude: []string{"legacy"}},
	})
	require.NoError(t, err)

	for _, present := range []string{"users.sql", "posts.sql"} {
		_, err := os.Stat(dir + "/" + present)
		assert.NoError(t, err, "%s should be written", present)
	}
	_, err = os.Stat(dir + "/legacy.sql")
	assert.True(t, os.IsNotExist(err), "legacy.sql should NOT be written")
}

// TestDump_SplitFileContentMatchesConcatMode: each per-object
// file holds exactly what concat-mode emits for that object,
// trailing newline included. Pinning the equality (not just a
// substring spot-check) means a future refactor that switches
// emitter for one mode but not the other would surface here.
func TestDump_SplitFileContentMatchesConcatMode(t *testing.T) {
	ctx := context.Background()
	conn := testutil.ConnectDB(t)
	testutil.SetupDB(t, ctx, conn, `
CREATE TABLE users (id BIGINT NOT NULL, PRIMARY KEY (id));
CREATE VIEW v_users AS SELECT id FROM users;
`)

	// Concat mode is the source of truth.
	c := newClient(t)
	concat, err := c.Dump(ctx, &myschema.DumpOptions{})
	require.NoError(t, err)

	// Split mode writes the same content per file.
	dir := t.TempDir()
	_, err = c.Dump(ctx, &myschema.DumpOptions{SplitDir: dir})
	require.NoError(t, err)

	usersFile, err := os.ReadFile(dir + "/users.sql")
	require.NoError(t, err)
	viewFile, err := os.ReadFile(dir + "/v_users.sql")
	require.NoError(t, err)

	// Each per-object file ends with a trailing newline so
	// `cat dir/*.sql` produces a clean concat-equivalent stream.
	assert.True(t, strings.HasSuffix(string(usersFile), "\n"), "table file must end with newline")
	assert.True(t, strings.HasSuffix(string(viewFile), "\n"), "view file must end with newline")

	// Concat mode separates objects with `\n\n`. Split files
	// individually carry one trailing newline; concatenated they
	// equal concat-mode output (modulo the join separator).
	combined := strings.TrimRight(string(usersFile), "\n") + "\n\n" +
		strings.TrimRight(string(viewFile), "\n")
	assert.Equal(t, strings.TrimRight(concat.SQL, "\n"), combined,
		"split files concatenated should match concat-mode body")
}

// TestDump_SplitEmptySchema: zero tables, zero views — directory
// gets created (mkdir -p) but no files inside.
func TestDump_SplitEmptySchema(t *testing.T) {
	ctx := context.Background()
	conn := testutil.ConnectDB(t)
	testutil.SetupDB(t, ctx, conn, "")

	dir := t.TempDir() + "/out"
	c := newClient(t)
	_, err := c.Dump(ctx, &myschema.DumpOptions{SplitDir: dir})
	require.NoError(t, err)
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, entries, "no objects → no files (but dir is created)")
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
