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
	currentViews, err := cat.Views(ctx)
	if err != nil {
		return nil, fmt.Errorf("fetch views: %w", err)
	}

	desired, err := parser.ParseSQLFiles(options.Files, database)
	if err != nil {
		return nil, fmt.Errorf("parse desired: %w", err)
	}

	currentTables = filterTables(currentTables, &options.FilterOptions)
	currentViews = filterViews(currentViews, &options.FilterOptions)
	desiredTables := filterTables(desired.Tables, &options.FilterOptions)
	desiredViews := filterViews(desired.Views, &options.FilterOptions)

	tableDiff, err := diff.DiffTables(currentTables, desiredTables, &options.DropPolicy)
	if err != nil {
		return nil, fmt.Errorf("diff tables: %w", err)
	}
	viewDiff, err := diff.DiffViews(currentViews, desiredViews, database, &options.DropPolicy)
	if err != nil {
		return nil, fmt.Errorf("diff views: %w", err)
	}

	// Order: view drops → table renames → FK drops → table create/alter
	// → table drops → FK adds → view create-or-replace.
	// Views must drop before columns / tables they reference are altered or
	// removed, and must (re)create only after the underlying tables exist.
	// Table renames must precede FK drops because the same combined
	// migration may target the table under its new name (the rename goes
	// first, then anything that ALTERs the new-named table).
	var stmts []string
	stmts = append(stmts, viewDiff.DropStmts...)
	stmts = append(stmts, tableDiff.RenameStmts...)
	stmts = append(stmts, tableDiff.FKDropStmts...)
	stmts = append(stmts, tableDiff.Stmts...)
	stmts = append(stmts, tableDiff.DropStmts...)
	stmts = append(stmts, tableDiff.FKAddStmts...)
	stmts = append(stmts, viewDiff.CreateStmts...)

	var disallowed []string
	disallowed = append(disallowed, tableDiff.DisallowedDropStmts...)
	disallowed = append(disallowed, viewDiff.DisallowedDropStmts...)

	count := ObjectCount{
		Database: database,
		Tables:   currentTables.Len(),
		Views:    currentViews.Len(),
	}

	return &diffAllResult{
		Stmts:           stmts,
		DisallowedDrops: disallowed,
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

func filterViews(in *orderedmap.Map[string, *model.View], f *FilterOptions) *orderedmap.Map[string, *model.View] {
	out := orderedmap.New[string, *model.View]()
	for k, v := range in.All() {
		if !f.MatchName(v.Name) {
			continue
		}
		out.Set(k, v)
	}
	return out
}
