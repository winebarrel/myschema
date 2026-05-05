package myschema

import (
	"context"
	"fmt"
	"io"
	"strings"
)

// ApplyOptions are the flags accepted by `myschema apply`.
type ApplyOptions struct {
	FilterOptions
	DropPolicy
	AlterOption
	PreSQLOption
	Files []string `arg:"" help:"Path to the desired schema SQL file(s)."`
}

// ApplyResult mirrors PlanResult but reports what was actually executed.
type ApplyResult struct {
	Count           ObjectCount
	DisallowedDrops string
}

// Apply runs the diff against the database, writing each executed statement
// to w as it goes.
func (c *Client) Apply(ctx context.Context, options *ApplyOptions, w io.Writer) (*ApplyResult, error) {
	database, err := c.Database()
	if err != nil {
		return nil, err
	}
	conn, err := c.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close() //nolint:errcheck

	if err := runPreSQL(ctx, conn.Conn, options.PreSQLOption); err != nil {
		return nil, err
	}

	r, err := c.diffAll(ctx, conn.Conn, database, &diffAllOptions{
		FilterOptions: options.FilterOptions,
		DropPolicy:    options.DropPolicy,
		AlterOption:   options.AlterOption,
		Files:         options.Files,
	})
	if err != nil {
		return nil, err
	}

	res := &ApplyResult{
		Count:           r.Count,
		DisallowedDrops: strings.Join(r.DisallowedDrops, "\n"),
	}

	for _, stmt := range r.Stmts {
		fmt.Fprintln(w, stmt) //nolint:errcheck
		if _, err := conn.ExecContext(ctx, stmt); err != nil {
			return nil, fmt.Errorf("execute %q: %w", stmt, err)
		}
	}
	return res, nil
}
