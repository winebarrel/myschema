package myschema

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/winebarrel/myschema/catalog"
	"github.com/winebarrel/myschema/diff"
	"github.com/winebarrel/myschema/model"
	"github.com/winebarrel/myschema/parser"
	"github.com/winebarrel/orderedmap"
)

type diffAllOptions struct {
	FilterOptions
	DropPolicy
	AlterOption
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

	// --alter-algorithm / --alter-lock append MySQL online-DDL hints
	// to every generated `ALTER TABLE …` and `CREATE INDEX …`
	// statement, with the per-DDL syntax (comma vs space separators)
	// applied automatically. Done after the buckets are merged so
	// every statement passes through the rewrite exactly once.
	stmts = appendAlterHints(stmts, options.AlterAlgorithm, options.AlterLock)

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

// appendAlterHints adds the user-supplied ALGORITHM= / LOCK= clauses
// just before the trailing `;` on every statement that begins with
// ALTER TABLE or CREATE [UNIQUE|FULLTEXT|SPATIAL] INDEX
// (case-insensitive, leading whitespace tolerated). The two DDLs need
// different separators: ALTER TABLE expects a leading comma plus
// comma-separated clauses, while CREATE INDEX wants the clauses
// separated by spaces with no leading comma. The function picks the
// right shape per statement; the user just supplies the values.
//
// CREATE TABLE is intentionally excluded — online-DDL hints don't
// apply to fresh-table creation. DROP TABLE, DROP VIEW, CREATE [OR
// REPLACE] VIEW, and any other statement shape pass through unchanged.
//
// When neither algorithm nor lock is set the function is a fast no-op.
func appendAlterHints(stmts []string, algorithm, lock string) []string {
	if algorithm == "" && lock == "" {
		return stmts
	}
	out := make([]string, len(stmts))
	for i, s := range stmts {
		out[i] = appendHintsTo(s, algorithm, lock)
	}
	return out
}

func appendHintsTo(stmt, algorithm, lock string) string {
	leading := strings.TrimLeft(stmt, " \t\n")
	upper := strings.ToUpper(leading)

	var sep, clauseSep string
	switch {
	case strings.HasPrefix(upper, "ALTER TABLE "):
		sep, clauseSep = ", ", ", "
	case strings.HasPrefix(upper, "CREATE INDEX "),
		strings.HasPrefix(upper, "CREATE UNIQUE INDEX "),
		strings.HasPrefix(upper, "CREATE FULLTEXT INDEX "),
		strings.HasPrefix(upper, "CREATE SPATIAL INDEX "):
		sep, clauseSep = " ", " "
	default:
		return stmt
	}

	var clauses []string
	if algorithm != "" {
		clauses = append(clauses, "ALGORITHM="+algorithm)
	}
	if lock != "" {
		clauses = append(clauses, "LOCK="+lock)
	}
	suffix := sep + strings.Join(clauses, clauseSep)
	if !strings.HasSuffix(stmt, ";") {
		return stmt + suffix
	}
	return strings.TrimSuffix(stmt, ";") + suffix + ";"
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
