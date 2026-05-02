// Package testutil provides a database/sql connection bound to the local MySQL
// test instance plus a SetupDB helper that resets a test database to a clean
// state before each test.
//
// The default DSN points at the docker-compose MySQL on 127.0.0.1:3306 with
// user root. Override with MYSCHEMA_TEST_DSN.
package testutil

import (
	"context"
	"database/sql"
	"os"
	"testing"

	mysqldrv "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"
)

// DefaultDB is the database tests recreate at the start of each run.
const DefaultDB = "myschema_test"

// ConnectDB opens a *sql.DB against the test MySQL instance. The connection is
// not bound to any database (DSN ends with `/`) so callers can decide which
// schema to use.
func ConnectDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("MYSCHEMA_TEST_DSN")
	if dsn == "" {
		dsn = "root@tcp(127.0.0.1:3306)/"
	}

	cfg, err := mysqldrv.ParseDSN(dsn)
	require.NoError(t, err)
	cfg.MultiStatements = true
	cfg.ParseTime = true

	db, err := sql.Open("mysql", cfg.FormatDSN())
	require.NoError(t, err)
	require.NoError(t, db.PingContext(context.Background()))
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// SetupDB drops the test database, recreates it, and (optionally) runs initSQL
// against it. Mirrors pistachio's SetupDB so tests in either project share the
// same shape.
func SetupDB(t *testing.T, ctx context.Context, db *sql.DB, initSQL string) {
	t.Helper()
	_, err := db.ExecContext(ctx, "DROP DATABASE IF EXISTS "+DefaultDB)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, "CREATE DATABASE "+DefaultDB)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, "USE "+DefaultDB)
	require.NoError(t, err)
	if initSQL != "" {
		_, err = db.ExecContext(ctx, initSQL)
		require.NoError(t, err, "init SQL failed")
	}
}
