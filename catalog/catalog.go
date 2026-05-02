// Package catalog reads the current schema state out of MySQL via
// information_schema. The result mirrors model.Table so it can be diffed
// directly against the parser output.
package catalog

import (
	"context"
	"database/sql"
	"fmt"
)

// Catalog is a database/sql connection bound to a list of databases. The
// per-object queries below scope themselves to those databases.
type Catalog struct {
	db        *sql.DB
	databases []string
}

// NewCatalog wraps an existing sql.DB.
func NewCatalog(db *sql.DB, databases []string) *Catalog {
	return &Catalog{db: db, databases: databases}
}

// dbPlaceholders builds "?, ?, ?" with the right number of placeholders and
// returns the args slice already typed for QueryContext.
func (c *Catalog) dbPlaceholders() (string, []any) {
	args := make([]any, len(c.databases))
	ph := ""
	for i, d := range c.databases {
		if i > 0 {
			ph += ","
		}
		ph += "?"
		args[i] = d
	}
	return ph, args
}

// pingDB exists so other catalog files can fail fast on connectivity issues.
func (c *Catalog) ping(ctx context.Context) error {
	if err := c.db.PingContext(ctx); err != nil {
		return fmt.Errorf("catalog: ping failed: %w", err)
	}
	return nil
}
