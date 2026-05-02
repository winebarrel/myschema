package command

import (
	"context"
	"fmt"
	"io"

	"github.com/winebarrel/myschema"
)

type Plan struct {
	myschema.PlanOptions
}

func (cmd *Plan) Run(ctx context.Context, client *myschema.Client, w io.Writer) error {
	r, err := client.Plan(ctx, &cmd.PlanOptions)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "-- Plan for %s (%s)\n", r.Count.DBLabel(), r.Count.Summary()) //nolint:errcheck
	if r.SQL == "" {
		if r.DisallowedDrops != "" {
			fmt.Fprintln(w, r.DisallowedDrops) //nolint:errcheck
		}
		fmt.Fprintln(w, "-- No changes") //nolint:errcheck
		return nil
	}
	fmt.Fprintln(w, r.SQL) //nolint:errcheck
	if r.DisallowedDrops != "" {
		fmt.Fprintln(w, r.DisallowedDrops) //nolint:errcheck
	}
	return nil
}
