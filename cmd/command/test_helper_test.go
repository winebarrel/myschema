package command_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	mysqldrv "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"

	"github.com/winebarrel/myschema"
	"github.com/winebarrel/myschema/internal/testutil"
)

// newTestClient mirrors test_helper_test.go's newClient — the Client has to
// be constructed from Options because that's what carries the DSN. We
// duplicate the wiring here (rather than export a helper) because cmd/command
// sits below the root package.
//
// Parses MYSCHEMA_TEST_DSN through the MySQL driver and rewrites DBName, so
// callers don't have to care whether the env DSN already carries a database
// (a `root@tcp(...)/some_db` env value would otherwise produce
// `root@tcp(...)/some_db/myschema_test` under naive string concat).
func newTestClient(t *testing.T) *myschema.Client {
	t.Helper()
	base := os.Getenv("MYSCHEMA_TEST_DSN")
	if base == "" {
		base = "root@tcp(127.0.0.1:3306)/"
	}
	cfg, err := mysqldrv.ParseDSN(strings.TrimSuffix(base, "/") + "/")
	require.NoError(t, err, "parse MYSCHEMA_TEST_DSN base %q", base)
	cfg.DBName = testutil.DefaultDB
	return myschema.NewClient(&myschema.Options{DSN: cfg.FormatDSN()})
}

func writeDesiredFile(t *testing.T, sql string) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "desired.sql")
	require.NoError(t, os.WriteFile(f, []byte(sql), 0o644))
	return f
}
