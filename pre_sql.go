package myschema

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"

	"vitess.io/vitess/go/vt/sqlparser"
)

// loadPreSQL resolves and validates the pre-SQL payload from
// --pre-sql / --pre-sql-file. It returns the raw SQL text (or empty
// when nothing is configured) without contacting MySQL — apply / plan
// call this BEFORE connect() so flag-validation errors aren't masked
// by a downstream connection failure (and the client doesn't open a
// DB connection just to find out the user passed both flags).
//
// `--pre-sql` and `--pre-sql-file` are mutually exclusive — keeps the
// precedence rule trivial. Whitespace-only values are treated as
// not-set so `MYSCHEMA_PRE_SQL=` (empty / whitespace) doesn't trip the
// mutually-exclusive check against a legitimate --pre-sql-file.
//
// stdin (`-`) is rejected for --pre-sql-file. The desired-SQL file
// args already accept `-`, so allowing it here would let both
// inputs fight over stdin (the second read would hit EOF and
// silently truncate). Pre-SQL is small enough that env / inline
// covers the no-real-file case; on-disk paths are the only file
// shape worth supporting.
func loadPreSQL(opt PreSQLOption) (string, error) {
	preSQL := strings.TrimSpace(opt.PreSQL)
	preFile := strings.TrimSpace(opt.PreSQLFile)
	if preSQL != "" && preFile != "" {
		return "", fmt.Errorf("pre-sql: --pre-sql and --pre-sql-file are mutually exclusive (got both)")
	}
	if preFile == "-" {
		return "", fmt.Errorf("pre-sql: --pre-sql-file=- (stdin) is not supported; use --pre-sql for inline payload or pass a file path")
	}
	if preSQL != "" {
		return preSQL, nil
	}
	if preFile != "" {
		data, err := os.ReadFile(preFile)
		if err != nil {
			return "", fmt.Errorf("pre-sql: read %s: %w", preFile, err)
		}
		return string(data), nil
	}
	return "", nil
}

// execPreSQL runs the pre-SQL text against conn. The caller is
// expected to have validated/loaded the payload via loadPreSQL
// before opening the connection. Empty payload is a no-op.
//
// Multi-statement payloads are split via vitess'
// SplitStatementToPieces (same splitter parser/parser.go uses for
// desired SQL) so a chain like
// `SET FOREIGN_KEY_CHECKS=0; SET sql_mode=<empty>` runs as two
// Exec calls in order. Splitting via vitess (rather than naive
// strings.Split on `;`) is what keeps `;` inside string literals
// from breaking the chain.
func execPreSQL(ctx context.Context, conn *sql.Conn, sqlText string) error {
	if strings.TrimSpace(sqlText) == "" {
		return nil
	}
	p, err := sqlparser.New(sqlparser.Options{})
	if err != nil {
		return fmt.Errorf("pre-sql: init parser: %w", err)
	}
	pieces, err := p.SplitStatementToPieces(sqlText)
	if err != nil {
		return fmt.Errorf("pre-sql: split: %w", err)
	}
	for _, piece := range pieces {
		stmt := strings.TrimSpace(piece)
		if stmt == "" {
			continue
		}
		if _, err := conn.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("pre-sql: execute %q: %w", stmt, err)
		}
	}
	return nil
}
