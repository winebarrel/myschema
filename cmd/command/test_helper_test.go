package command_test

import (
	"os"
	"path/filepath"
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
// Parses MYSCHEMA_TEST_DSN as-is through ParseDSN and overwrites DBName, so
// the test DB is always the only DBName regardless of what the env DSN
// started with. No string normalisation: a `TrimSuffix + "/"` would corrupt
// query params on inputs like `root@tcp(...)/?parseTime=true`.
func newTestClient(t *testing.T) *myschema.Client {
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
	cfg.DBName = testutil.DefaultDB
	return myschema.NewClient(&myschema.Options{DSN: cfg.FormatDSN()})
}

func writeDesiredFile(t *testing.T, sql string) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "desired.sql")
	require.NoError(t, os.WriteFile(f, []byte(sql), 0o644))
	return f
}
