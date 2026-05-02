// Package testutil provides a single dedicated MySQL connection bound to the
// local test instance plus a SetupDB helper that resets a test database to a
// clean state before each test. Pooling is intentionally disabled so `USE
// <db>` and other session-scoped statements behave the same way the
// production code does.
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

// ConnectDB opens a *sql.DB, caps the pool to one connection, and returns a
// dedicated *sql.Conn. Both are closed via t.Cleanup so callers don't have to
// track lifetimes. The DSN is not bound to any database (ends with `/`) so
// callers can decide which schema to use.
func ConnectDB(t *testing.T) *sql.Conn {
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
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	conn, err := db.Conn(context.Background())
	if err != nil {
		_ = db.Close()
		require.NoError(t, err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
		_ = db.Close()
	})
	return conn
}

// SetupDB drops the test database, recreates it, and (optionally) runs initSQL
// against it. Mirrors pistachio's SetupDB so tests in either project share the
// same shape.
func SetupDB(t *testing.T, ctx context.Context, conn *sql.Conn, initSQL string) {
	t.Helper()
	_, err := conn.ExecContext(ctx, "DROP DATABASE IF EXISTS "+DefaultDB)
	require.NoError(t, err)
	_, err = conn.ExecContext(ctx, "CREATE DATABASE "+DefaultDB)
	require.NoError(t, err)
	_, err = conn.ExecContext(ctx, "USE "+DefaultDB)
	require.NoError(t, err)
	if initSQL != "" {
		_, err = conn.ExecContext(ctx, initSQL)
		require.NoError(t, err, "init SQL failed")
	}
}
