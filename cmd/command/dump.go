package command

import (
	"context"
	"fmt"
	"io"

	"github.com/winebarrel/myschema"
)

type Dump struct {
	myschema.DumpOptions
}

func (cmd *Dump) Run(ctx context.Context, client *myschema.Client, w io.Writer) error {
	r, err := client.Dump(ctx, &cmd.DumpOptions)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "-- Dump of %s (%s)\n", r.Count.DBLabel(), r.Count.Summary()) //nolint:errcheck
	if cmd.SplitDir != "" {
		// SQL body lives in the per-object files; only print the
		// completion notice so the user knows where they landed.
		fmt.Fprintf(w, "-- Wrote %d file(s) to %s\n", //nolint:errcheck
			r.Count.Tables+r.Count.Views, cmd.SplitDir)
		return nil
	}
	fmt.Fprintln(w, r) //nolint:errcheck
	return nil
}
