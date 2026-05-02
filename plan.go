package myschema

import (
	"context"
	"strings"
)

// PlanOptions are the flags accepted by `myschema plan`.
type PlanOptions struct {
	FilterOptions
	DropPolicy
	Files []string `arg:"" help:"Path to the desired schema SQL file(s)."`
}

// PlanResult is what `myschema plan` returns to the caller.
type PlanResult struct {
	SQL             string
	DisallowedDrops string
	Count           ObjectCount
}

// Plan computes the diff and returns it as a single SQL string.
func (c *Client) Plan(ctx context.Context, options *PlanOptions) (*PlanResult, error) {
	conn, err := c.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close() //nolint:errcheck

	r, err := c.diffAll(ctx, conn.Conn, &diffAllOptions{
		FilterOptions: options.FilterOptions,
		DropPolicy:    options.DropPolicy,
		Files:         options.Files,
	})
	if err != nil {
		return nil, err
	}
	return &PlanResult{
		SQL:             strings.Join(r.Stmts, "\n"),
		DisallowedDrops: strings.Join(r.DisallowedDrops, "\n"),
		Count:           r.Count,
	}, nil
}
