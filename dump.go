package myschema

import (
	"context"
	"strings"

	"github.com/winebarrel/myschema/catalog"
	"github.com/winebarrel/myschema/model"
)

// DumpOptions are the flags accepted by `myschema dump`.
type DumpOptions struct {
	FilterOptions
}

// DumpResult is the rendered current-schema SQL plus a count for the header.
type DumpResult struct {
	SQL   string
	Count ObjectCount
}

// String makes DumpResult fmt.Stringer-friendly so callers can write it directly.
func (d *DumpResult) String() string { return d.SQL }

// Dump reads the current schema from MySQL and returns it as a SQL string.
func (c *Client) Dump(ctx context.Context, options *DumpOptions) (*DumpResult, error) {
	database, err := c.Database()
	if err != nil {
		return nil, err
	}
	conn, err := c.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close() //nolint:errcheck

	cat := catalog.NewCatalog(conn.Conn, database)
	tables, err := cat.Tables(ctx)
	if err != nil {
		return nil, err
	}
	views, err := cat.Views(ctx)
	if err != nil {
		return nil, err
	}

	tables = filterTables(tables, &options.FilterOptions)
	views = filterViews(views, &options.FilterOptions)

	parts := []string{model.TablesToSQL(tables)}
	if views.Len() > 0 {
		parts = append(parts, model.ViewsToSQL(views))
	}
	nonEmpty := parts[:0]
	for _, p := range parts {
		if p != "" {
			nonEmpty = append(nonEmpty, p)
		}
	}
	sql := strings.Join(nonEmpty, "\n\n")

	return &DumpResult{
		SQL: sql,
		Count: ObjectCount{
			Database: database,
			Tables:   tables.Len(),
			Views:    views.Len(),
		},
	}, nil
}
