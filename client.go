package myschema

import (
	"database/sql"
	"fmt"

	mysqldrv "github.com/go-sql-driver/mysql"
)

// Client is a thin wrapper around the global Options + a sql.DB factory.
// Each subcommand opens and closes its own connection.
type Client struct {
	*Options
}

// NewClient binds a Client to the parsed CLI options.
func NewClient(o *Options) *Client {
	return &Client{Options: o}
}

func (c *Client) connect() (*sql.DB, error) {
	cfg, err := mysqldrv.ParseDSN(c.DSN)
	if err != nil {
		return nil, fmt.Errorf("myschema: parse DSN: %w", err)
	}
	if c.Password != "" {
		cfg.Passwd = c.Password
	}
	cfg.MultiStatements = true
	cfg.ParseTime = true

	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return nil, fmt.Errorf("myschema: open: %w", err)
	}
	return db, nil
}
