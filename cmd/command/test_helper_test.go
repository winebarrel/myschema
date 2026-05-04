package command_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/winebarrel/myschema"
	"github.com/winebarrel/myschema/internal/testutil"
)

// newTestClient mirrors test_helper_test.go's newClient — the Client has to
// be constructed from Options because that's what carries the DSN. We
// duplicate the wiring here (rather than export a helper) because cmd/command
// sits below the root package.
func newTestClient(t *testing.T) *myschema.Client {
	t.Helper()
	base := os.Getenv("MYSCHEMA_TEST_DSN")
	if base == "" {
		base = "root@tcp(127.0.0.1:3306)/"
	}
	if !strings.HasSuffix(base, "/") {
		base += "/"
	}
	return myschema.NewClient(&myschema.Options{
		DSN: base + testutil.DefaultDB,
	})
}

func writeDesiredFile(t *testing.T, sql string) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "desired.sql")
	require.NoError(t, os.WriteFile(f, []byte(sql), 0o644))
	return f
}
