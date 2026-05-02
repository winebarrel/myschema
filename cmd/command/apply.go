package command

import (
	"bytes"
	"context"
	"fmt"
	"io"

	"github.com/winebarrel/myschema"
)

type Apply struct {
	myschema.ApplyOptions
}

func (cmd *Apply) Run(ctx context.Context, client *myschema.Client, w io.Writer) error {
	var buf bytes.Buffer
	r, err := client.Apply(ctx, &cmd.ApplyOptions, &buf)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "-- Apply to %s (%s)\n", r.Count.DBLabel(), r.Count.Summary()) //nolint:errcheck
	if buf.Len() == 0 {
		if r.DisallowedDrops != "" {
			fmt.Fprintln(w, r.DisallowedDrops) //nolint:errcheck
		}
		fmt.Fprintln(w, "-- No changes") //nolint:errcheck
		return nil
	}
	w.Write(buf.Bytes()) //nolint:errcheck
	if r.DisallowedDrops != "" {
		fmt.Fprintln(w, r.DisallowedDrops) //nolint:errcheck
	}
	return nil
}
