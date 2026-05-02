package myschema

import (
	"context"
	"database/sql"
	"fmt"

	mysqldrv "github.com/go-sql-driver/mysql"
)

// Client is a thin wrapper around the global Options + a sql.Conn factory.
// Each subcommand acquires a single dedicated connection (no pool) and
// closes it on exit.
type Client struct {
	*Options
}

// NewClient binds a Client to the parsed CLI options.
func NewClient(o *Options) *Client {
	return &Client{Options: o}
}

// connection wraps a *sql.Conn with the *sql.DB it was acquired from so the
// caller can close both with one Close().
type connection struct {
	*sql.Conn
	db *sql.DB
}

// Close releases the connection back to its parent *sql.DB and closes the DB.
func (c *connection) Close() error {
	cerr := c.Conn.Close()
	derr := c.db.Close()
	if cerr != nil {
		return cerr
	}
	return derr
}

// dsnConfig parses Options.DSN, applies the optional Password override, and
// validates that the DSN names a database. Used by both Database() and
// connect() so the parse / validation happens in one place.
func (c *Client) dsnConfig() (*mysqldrv.Config, error) {
	cfg, err := mysqldrv.ParseDSN(c.DSN)
	if err != nil {
		return nil, fmt.Errorf("myschema: parse DSN: %w", err)
	}
	if cfg.DBName == "" {
		return nil, fmt.Errorf("myschema: MYSCHEMA_DSN must include a database name (e.g. root@tcp(127.0.0.1:3306)/mydb)")
	}
	if c.Password != "" {
		cfg.Passwd = c.Password
	}
	return cfg, nil
}

// Database returns the database name baked into the DSN.
func (c *Client) Database() (string, error) {
	cfg, err := c.dsnConfig()
	if err != nil {
		return "", err
	}
	return cfg.DBName, nil
}

// connect opens a *sql.DB, immediately reserves a single dedicated connection,
// and returns it. The pool is sized to one so no other code path can borrow
// a second connection by accident. We do NOT enable MultiStatements here;
// apply runs each statement individually and a single-statement-per-Exec
// surface area is safer (e.g. against operator paste of `;`-laden input).
func (c *Client) connect(ctx context.Context) (*connection, error) {
	cfg, err := c.dsnConfig()
	if err != nil {
		return nil, err
	}
	cfg.ParseTime = true

	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return nil, fmt.Errorf("myschema: open: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	conn, err := db.Conn(ctx)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("myschema: acquire connection: %w", err)
	}
	return &connection{Conn: conn, db: db}, nil
}
