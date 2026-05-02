package myschema_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/winebarrel/myschema"
	"github.com/winebarrel/myschema/internal/testutil"
	"gopkg.in/yaml.v3"
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
// default) and scoped to the test database.
func newClient(t *testing.T) *myschema.Client {
	t.Helper()
	dsn := os.Getenv("MYSCHEMA_TEST_DSN")
	if dsn == "" {
		dsn = "root@tcp(127.0.0.1:3306)/"
	}
	return myschema.NewClient(&myschema.Options{
		DSN:       dsn,
		Databases: []string{testutil.DefaultDB},
	})
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
