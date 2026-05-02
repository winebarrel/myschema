// Package catalog reads the current schema state out of MySQL via
// information_schema. The result mirrors model.Table so it can be diffed
// directly against the parser output.
package catalog

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// Catalog is a single dedicated MySQL connection bound to a list of
// databases. Per-object queries below scope themselves to those databases.
// The connection is single-use by design (no pooling); callers manage its
// lifetime via *sql.Conn.Close().
type Catalog struct {
	conn      *sql.Conn
	databases []string
}

// NewCatalog wraps an existing *sql.Conn.
func NewCatalog(conn *sql.Conn, databases []string) *Catalog {
	return &Catalog{conn: conn, databases: databases}
}

// dbPlaceholders builds "?, ?, ?" with the right number of placeholders and
// returns the args slice already typed for QueryContext.
func (c *Catalog) dbPlaceholders() (string, []any) {
	args := make([]any, len(c.databases))
	parts := make([]string, len(c.databases))
	for i, d := range c.databases {
		parts[i] = "?"
		args[i] = d
	}
	return strings.Join(parts, ","), args
}

// ping fails fast on a dead connection.
func (c *Catalog) ping(ctx context.Context) error {
	if err := c.conn.PingContext(ctx); err != nil {
		return fmt.Errorf("catalog: ping failed: %w", err)
	}
	return nil
}
