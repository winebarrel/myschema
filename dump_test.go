package myschema_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/alecthomas/kong"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/winebarrel/orderedmap"

	"github.com/winebarrel/myschema"
	"github.com/winebarrel/myschema/internal/testutil"
	"github.com/winebarrel/myschema/model"
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

// TestDump_SplitFlagName pins the kong-derived CLI flag name as
// `--split` (not the default `--split-dir` kong would derive from
// the SplitDir field). Documentation and PR history say `--split=<dir>`;
// a future field rename or a dropped `name:"split"` tag would silently
// shift the user-facing flag, so parse the flag through real kong
// here to keep the surface in lockstep with the docs.
func TestDump_SplitFlagName(t *testing.T) {
	var cli struct {
		myschema.DumpOptions
	}
	parser, err := kong.New(&cli)
	require.NoError(t, err)
	_, err = parser.Parse([]string{"--split=/tmp/out"})
	require.NoError(t, err, "kong must accept --split=<dir>")
	assert.Equal(t, "/tmp/out", cli.SplitDir)

	// And the legacy auto-derived `--split-dir` must NOT work — if
	// it did, both names would coexist and docs would drift.
	_, err = parser.Parse([]string{"--split-dir=/tmp/out"})
	require.Error(t, err, "kong must reject --split-dir (renamed to --split)")
}

// TestDump_SplitRejectsUnsafeName pins the path-traversal guard in
// splitPath. MySQL itself rejects '/', '\', '.' in identifiers, so
// these cases are defence-in-depth: if a future catalog change or a
// non-MySQL data source ever fed an unsafe name through, the writer
// must refuse rather than scribble outside the requested directory.
func TestDump_SplitRejectsUnsafeName(t *testing.T) {
	cases := []struct {
		name string
		obj  string
	}{
		{"empty", ""},
		{"single dot", "."},
		{"double dot", ".."},
		{"forward slash", "a/b"},
		{"backslash", `a\b`},
		{"traversal", "../escape"},
		{"absolute", "/etc/passwd"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := myschema.SplitPath(t.TempDir(), tc.obj)
			require.Error(t, err, "splitPath(%q) must reject", tc.obj)
			assert.Contains(t, err.Error(), "unsafe object name")
		})
	}
}

// TestDump_SplitPath_Valid pins the happy path: a normal identifier
// (incl. underscore, $, digits) returns dir/name.sql.
func TestDump_SplitPath_Valid(t *testing.T) {
	dir := t.TempDir()
	got, err := myschema.SplitPath(dir, "my_table$1")
	require.NoError(t, err)
	assert.Equal(t, dir+"/my_table$1.sql", got)
}

// TestWriteDumpSplit_RejectsTableWithUnsafeName: defence-in-depth.
// MySQL forbids these characters at the source, so the only way an
// unsafe name reaches writeDumpSplit is via a non-MySQL data source
// (mocks, tests, or a future upstream change). Pin the in-loop
// validator branch so a future refactor can't silently bypass it.
func TestWriteDumpSplit_RejectsTableWithUnsafeName(t *testing.T) {
	dir := t.TempDir()
	tables := orderedmap.New[string, *model.Table]()
	tables.Set("evil", &model.Table{Name: "../escape"})
	views := orderedmap.New[string, *model.View]()
	err := myschema.WriteDumpSplit(dir, tables, views)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsafe object name")
}

// TestWriteDumpSplit_RejectsViewWithUnsafeName: same as the table
// guard above, on the view loop.
func TestWriteDumpSplit_RejectsViewWithUnsafeName(t *testing.T) {
	dir := t.TempDir()
	tables := orderedmap.New[string, *model.Table]()
	views := orderedmap.New[string, *model.View]()
	views.Set("evil", &model.View{Name: "a/b"})
	err := myschema.WriteDumpSplit(dir, tables, views)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsafe object name")
}

// emptyTable returns a Table whose maps are initialised so
// model.TableToSQL won't panic during a write-error test.
func emptyTable(name string) *model.Table {
	return &model.Table{
		Name:        name,
		Columns:     orderedmap.New[string, *model.Column](),
		Constraints: orderedmap.New[string, *model.Constraint](),
		ForeignKeys: orderedmap.New[string, *model.ForeignKey](),
		Indexes:     orderedmap.New[string, *model.Index](),
	}
}

// TestWriteDumpSplit_TableWriteError: when the per-table file path
// already exists as a *directory*, os.WriteFile fails with EISDIR.
// The error must surface up wrapped with the path so the operator
// can tell which file failed (vs. a generic mkdir / IO problem).
func TestWriteDumpSplit_TableWriteError(t *testing.T) {
	dir := t.TempDir()
	// Pre-create a *directory* at <dir>/t.sql so os.WriteFile fails.
	require.NoError(t, os.MkdirAll(dir+"/t.sql", 0o755))
	tables := orderedmap.New[string, *model.Table]()
	tables.Set("t", emptyTable("t"))
	views := orderedmap.New[string, *model.View]()
	err := myschema.WriteDumpSplit(dir, tables, views)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dump: write")
	assert.Contains(t, err.Error(), "t.sql")
}

// TestWriteDumpSplit_ViewWriteError: same as the table-write-error
// guard above but on the view branch.
func TestWriteDumpSplit_ViewWriteError(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(dir+"/v.sql", 0o755))
	tables := orderedmap.New[string, *model.Table]()
	views := orderedmap.New[string, *model.View]()
	views.Set("v", &model.View{Name: "v", Definition: "SELECT 1"})
	err := myschema.WriteDumpSplit(dir, tables, views)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dump: write")
	assert.Contains(t, err.Error(), "v.sql")
}

// TestDump_SplitMkdirOverFile: when the caller points --split at an
// existing regular file, MkdirAll fails with "not a directory" and
// the error must surface up through Dump rather than silently no-op'ing.
func TestDump_SplitMkdirOverFile(t *testing.T) {
	dir := t.TempDir()
	// Create a regular file at the path we'll then ask --split to use.
	clash := dir + "/not_a_dir"
	require.NoError(t, os.WriteFile(clash, []byte("x"), 0o600))

	ctx := context.Background()
	conn := testutil.ConnectDB(t)
	testutil.SetupDB(t, ctx, conn, `CREATE TABLE t (id INT NOT NULL, PRIMARY KEY (id));`)
	c := newClient(t)
	_, err := c.Dump(ctx, &myschema.DumpOptions{SplitDir: clash})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mkdir")
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
