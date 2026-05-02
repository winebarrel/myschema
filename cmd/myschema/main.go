package main

import (
	"context"
	"io"
	"os"

	"github.com/alecthomas/kong"
	"github.com/winebarrel/myschema"
	"github.com/winebarrel/myschema/cmd/command"
)

var version = "dev"

var cli struct {
	myschema.Options
	Version kong.VersionFlag

	Apply command.Apply `cmd:"" help:"Apply schema changes to the database."`
	Plan  command.Plan  `cmd:"" help:"Print the schema diff SQL without applying it."`
	Dump  command.Dump  `cmd:"" help:"Dump the current database schema as SQL."`
}

func main() {
	ctx := context.Background()
	kctx := kong.Parse(&cli,
		kong.Vars{"version": version},
		kong.BindTo(ctx, (*context.Context)(nil)),
		kong.BindTo(os.Stdout, (*io.Writer)(nil)),
	)
	client := myschema.NewClient(&cli.Options)
	err := kctx.Run(client)
	kctx.FatalIfErrorf(err)
}
