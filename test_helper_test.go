package myschema_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	mysqldrv "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/winebarrel/myschema"
	"github.com/winebarrel/myschema/internal/testutil"
)

// runYAMLCases discovers every *.yml file under dir, decodes it into a fresh
// instance of T (one per fixture), and runs body inside a t.Run subtest named
// after the fixture's basename. Fixtures with .yml extension only.
func runYAMLCases[T any](t *testing.T, dir string, body func(t *testing.T, name string, tc *T)) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yml") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		name := strings.TrimSuffix(e.Name(), ".yml")
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			var tc T
			require.NoError(t, yaml.Unmarshal(data, &tc), "decode %s", path)
			body(t, name, &tc)
		})
	}
}

// newClient returns a Client wired to MYSCHEMA_TEST_DSN (or the docker-compose
// default). The DSN is parsed and DBName is overwritten with testutil.DefaultDB,
// since myschema requires the DSN itself to carry the database name.
func newClient(t *testing.T) *myschema.Client {
	t.Helper()
	return newClientWithDB(t, testutil.DefaultDB)
}

// newClientWithDB is the underlying constructor: it parses MYSCHEMA_TEST_DSN
// (or the docker-compose default) and rewrites the DBName field to dbName,
// so callers don't have to care whether the env DSN already carried a
// database. Used by newClient for the test-DB happy path and by error-path
// tests that need to point at a guaranteed-nonexistent database on the
// same MySQL instance the rest of the suite is using (so the MySQL 9.x CI
// leg, which sets MYSCHEMA_TEST_DSN to port 3307, doesn't connect to 3306
// by accident).
//
// The env DSN is passed to ParseDSN as-is — no string normalisation,
// because doing so would corrupt query params on inputs like
// `root@tcp(...)/?parseTime=true` (a trailing `/` would land *inside*
// the query string).
func newClientWithDB(t *testing.T, dbName string) *myschema.Client {
	t.Helper()
	base := os.Getenv("MYSCHEMA_TEST_DSN")
	if base == "" {
		base = "root@tcp(127.0.0.1:3306)/"
	}
	cfg, err := mysqldrv.ParseDSN(base)
	// Don't echo `base` in the failure message: MYSCHEMA_TEST_DSN can
	// embed a password and would leak into CI logs. The wrapped
	// ParseDSN error is enough to identify the problem.
	require.NoError(t, err, "parse MYSCHEMA_TEST_DSN (must be a valid DSN, e.g. 'root@tcp(127.0.0.1:3306)/')")
	cfg.DBName = dbName
	return myschema.NewClient(&myschema.Options{DSN: cfg.FormatDSN()})
}

// writeDesired writes desired-state SQL to a temp .sql file and returns its
// path, suitable for passing as Files: [...].
func writeDesired(t *testing.T, sql string) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "desired.sql")
	require.NoError(t, os.WriteFile(f, []byte(sql), 0o644))
	return f
}

// setupCtx is a per-test context.Background() shortcut.
func setupCtx() context.Context { return context.Background() }
