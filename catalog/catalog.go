// Package catalog reads the current schema state out of MySQL via
// information_schema. The result mirrors model.Table so it can be diffed
// directly against the parser output.
package catalog

import (
	"context"
	"database/sql"
	"fmt"
)

// Catalog is a single dedicated MySQL connection bound to one database.
// Per-object queries below scope themselves to that database. The connection
// is single-use by design (no pooling); callers manage its lifetime via
// *sql.Conn.Close().
type Catalog struct {
	conn     *sql.Conn
	database string
}

// NewCatalog wraps an existing *sql.Conn bound to database.
func NewCatalog(conn *sql.Conn, database string) *Catalog {
	return &Catalog{conn: conn, database: database}
}

// ping fails fast on a dead connection.
func (c *Catalog) ping(ctx context.Context) error {
	if err := c.conn.PingContext(ctx); err != nil {
		return fmt.Errorf("catalog: ping failed: %w", err)
	}
	return nil
}
