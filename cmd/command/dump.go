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
	fmt.Fprintln(w, r)                                                            //nolint:errcheck
	return nil
}
