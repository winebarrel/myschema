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

	// `-- myschema:execute <check-sql>` blocks run after every other
	// DDL bucket so triggers / routines / events created via the
	// directive can refer to brand-new tables. For each block:
	//   - run the check SQL against the live database
	//   - 1+ rows → consider already-applied, push the SQL into
	//     disallowed as a `-- skipped: (myschema:execute check
	//     matched) <sql>` comment so users still see what was
	//     suppressed
	//   - 0 rows → push the execute SQL into stmts so plan prints it
	//     and apply runs it
	// The check itself is a SELECT so plan / apply both poll it; this
	// is intentional — the directive is the only way myschema talks to
	// objects it doesn't model, so "what's already there" can only be
	// answered by asking the server.
	for _, eg := range desired.Executes {
		applied, err := executeCheckMatched(ctx, conn, eg.CheckSQL)
		if err != nil {
			return nil, fmt.Errorf("-- myschema:execute check failed: %w", err)
		}
		if applied {
			// The skip line goes into the disallowed-drops bucket,
			// which the CLI prints one statement per line. A multi-
			// line ExecuteSQL (CREATE TRIGGER … BEGIN … END is the
			// motivating case) would otherwise leak its body lines
			// past the leading `--`. Replace `\n`, `\r`, and `\t`
			// (CRLF endings included) with single spaces —
			// strings.Fields would also collapse whitespace inside
			// string literals and comments, which would misrepresent
			// what's actually in the desired SQL.
			oneLine := strings.NewReplacer("\n", " ", "\r", " ", "\t", " ").Replace(eg.ExecuteSQL)
			disallowed = append(disallowed, "-- skipped: (myschema:execute check matched) "+oneLine)
			continue
		}
		stmts = append(stmts, eg.ExecuteSQL)
	}

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

// executeCheckMatched runs the check-SQL of a `-- myschema:execute`
// directive and returns true when the result set has at least one
// row — meaning the guarded execute SQL has already been applied
// and should be skipped. The function never inspects column values;
// only row presence matters.
func executeCheckMatched(ctx context.Context, conn *sql.Conn, checkSQL string) (bool, error) {
	rows, err := conn.QueryContext(ctx, checkSQL)
	if err != nil {
		return false, fmt.Errorf("query %q: %w", checkSQL, err)
	}
	defer rows.Close() //nolint:errcheck
	matched := rows.Next()
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("iterate %q: %w", checkSQL, err)
	}
	return matched, nil
}

// appendAlterHints adds the user-supplied ALGORITHM= / LOCK= clauses
// to every statement that begins with ALTER TABLE or
// CREATE [UNIQUE|FULLTEXT|SPATIAL] INDEX (case-insensitive,
// leading whitespace tolerated). The exact splice point depends on
// the statement shape:
//   - CREATE INDEX: append the clauses (space-separated, no leading
//     comma) just before the trailing `;`.
//   - ALTER TABLE column / index / constraint changes (ADD COLUMN,
//     DROP INDEX, MODIFY COLUMN, …): append `, ALGORITHM=…, LOCK=…`
//     just before the trailing `;`. MySQL parses these as
//     comma-separated alter_specifications and tolerates the
//     trailing position.
//   - ALTER TABLE *partition* operations (REORGANIZE / ADD / DROP /
//     COALESCE / TRUNCATE / EXCHANGE PARTITION): MySQL's parser
//     rejects the trailing-comma form (Error 1064 — the comma after
//     `INTO (…)` or after the partition list is treated as a new
//     partition_definition). Insert hints between the table name
//     and the partition-op keyword instead, so the output reads
//     `ALTER TABLE t ALGORITHM=COPY, LOCK=SHARED, REORGANIZE
//     PARTITION p1 INTO (…)`.
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

// partitionOpKeywords are the alter_specification keywords whose
// MySQL grammar reject `, ALGORITHM=…, LOCK=…` appended at the
// trailing-comma position. For these statements `appendHintsTo`
// inserts the hints between the table name and the keyword
// instead. Each entry includes a leading space so the substring
// search can't match a column / index name that happens to end in
// these letters.
var partitionOpKeywords = []string{
	" REORGANIZE PARTITION",
	" ADD PARTITION",
	" DROP PARTITION",
	" COALESCE PARTITION",
	" TRUNCATE PARTITION",
	" EXCHANGE PARTITION",
}

func appendHintsTo(stmt, algorithm, lock string) string {
	leading := strings.TrimLeft(stmt, " \t\n")
	upper := strings.ToUpper(leading)

	var clauses []string
	if algorithm != "" {
		clauses = append(clauses, "ALGORITHM="+algorithm)
	}
	if lock != "" {
		clauses = append(clauses, "LOCK="+lock)
	}

	switch {
	case strings.HasPrefix(upper, "ALTER TABLE "):
		if pos := partitionOpInsertPos(stmt, upper); pos > 0 {
			// Insert before the partition-op keyword.
			return stmt[:pos] + strings.Join(clauses, ", ") + ", " + stmt[pos:]
		}
		// Trailing-comma position is fine for column / index /
		// constraint alter_specifications.
		return appendBeforeSemicolon(stmt, ", "+strings.Join(clauses, ", "))
	case strings.HasPrefix(upper, "CREATE INDEX "),
		strings.HasPrefix(upper, "CREATE UNIQUE INDEX "),
		strings.HasPrefix(upper, "CREATE FULLTEXT INDEX "),
		strings.HasPrefix(upper, "CREATE SPATIAL INDEX "):
		return appendBeforeSemicolon(stmt, " "+strings.Join(clauses, " "))
	}
	return stmt
}

func partitionOpInsertPos(stmt, upper string) int {
	best := -1
	for _, kw := range partitionOpKeywords {
		idx := strings.Index(upper, kw)
		if idx < 0 {
			continue
		}
		// idx points at the leading space; insert AT that space so
		// the resulting SQL reads `ALTER TABLE t <hints>, <KEYWORD>`.
		if best < 0 || idx < best {
			best = idx + 1 // skip the leading space — we add our own ", "
		}
	}
	_ = stmt // index is the same in stmt and upper (same length)
	return best
}

func appendBeforeSemicolon(stmt, suffix string) string {
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
