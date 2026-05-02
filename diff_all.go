package myschema

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/winebarrel/myschema/catalog"
	"github.com/winebarrel/myschema/diff"
	"github.com/winebarrel/myschema/model"
	"github.com/winebarrel/myschema/parser"
	"github.com/winebarrel/orderedmap"
)

type diffAllOptions struct {
	FilterOptions
	DropPolicy
	Files []string
}

type diffAllResult struct {
	Stmts           []string
	DisallowedDrops []string
	Count           ObjectCount
}

func (c *Client) diffAll(ctx context.Context, conn *sql.Conn, database string, options *diffAllOptions) (*diffAllResult, error) {
	cat := catalog.NewCatalog(conn, database)

	currentTables, err := cat.Tables(ctx)
	if err != nil {
		return nil, fmt.Errorf("fetch tables: %w", err)
	}

	desired, err := parser.ParseSQLFiles(options.Files, database)
	if err != nil {
		return nil, fmt.Errorf("parse desired: %w", err)
	}

	currentTables = filterTables(currentTables, &options.FilterOptions)
	desiredTables := filterTables(desired.Tables, &options.FilterOptions)

	tableDiff, err := diff.DiffTables(currentTables, desiredTables, &options.DropPolicy)
	if err != nil {
		return nil, fmt.Errorf("diff: %w", err)
	}

	var stmts []string
	stmts = append(stmts, tableDiff.FKDropStmts...)
	stmts = append(stmts, tableDiff.Stmts...)
	stmts = append(stmts, tableDiff.DropStmts...)
	stmts = append(stmts, tableDiff.FKAddStmts...)

	count := ObjectCount{Database: database, Tables: currentTables.Len()}

	return &diffAllResult{
		Stmts:           stmts,
		DisallowedDrops: tableDiff.DisallowedDropStmts,
		Count:           count,
	}, nil
}

func filterTables(in *orderedmap.Map[string, *model.Table], f *FilterOptions) *orderedmap.Map[string, *model.Table] {
	out := orderedmap.New[string, *model.Table]()
	for k, v := range in.All() {
		if !f.MatchName(v.Name) {
			continue
		}
		out.Set(k, v)
	}
	return out
}
