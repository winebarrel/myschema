package myschema

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"

	"vitess.io/vitess/go/vt/sqlparser"
)

// runPreSQL executes the pre-SQL payload (inline string or file)
// against conn before the caller does any other work. Both sources
// are mutually exclusive and the function fails early on conflict.
// Empty payload is a no-op. Multi-statement payloads are split via
// vitess' SplitStatementToPieces (same splitter as the parser uses
// for desired SQL) so a chain like
// `SET FOREIGN_KEY_CHECKS=0; SET sql_mode=<empty>` runs as two
// Exec calls in order. Splitting via vitess (rather than naive
// strings.Split on `;`) is what keeps `;` inside string literals
// from breaking the chain.
func runPreSQL(ctx context.Context, conn *sql.Conn, opt PreSQLOption) error {
	sqlText, err := loadPreSQL(opt)
	if err != nil {
		return err
	}
	if sqlText == "" {
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

// loadPreSQL resolves the payload from --pre-sql / --pre-sql-file,
// rejecting the both-set case with a clear "mutually exclusive"
// error so the user doesn't have to guess which one wins.
func loadPreSQL(opt PreSQLOption) (string, error) {
	if opt.PreSQL != "" && opt.PreSQLFile != "" {
		return "", fmt.Errorf("pre-sql: --pre-sql and --pre-sql-file are mutually exclusive (got both)")
	}
	if opt.PreSQL != "" {
		return opt.PreSQL, nil
	}
	if opt.PreSQLFile != "" {
		data, err := os.ReadFile(opt.PreSQLFile)
		if err != nil {
			return "", fmt.Errorf("pre-sql: read %s: %w", opt.PreSQLFile, err)
		}
		return string(data), nil
	}
	return "", nil
}
