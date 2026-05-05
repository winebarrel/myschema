package myschema

import (
	"fmt"
	"path"
)

// Options are the global flags shared by every CLI subcommand. The DSN is
// the single source of truth for the target database — myschema does not
// model a separate "database list" parameter.
type Options struct {
	DSN      string `short:"d" env:"MYSCHEMA_DSN" required:"" help:"MySQL DSN including database name (e.g. root@tcp(127.0.0.1:3306)/mydb). See https://github.com/go-sql-driver/mysql#dsn-data-source-name"`
	Password string `env:"MYSCHEMA_PASSWORD" help:"MySQL password (overrides DSN)."`
}

// FilterOptions narrow the set of objects considered by plan/apply/dump.
type FilterOptions struct {
	Include []string `short:"I" env:"MYSCHEMA_INCLUDE" help:"Include only tables matching the pattern (wildcard: *, ?)."`
	Exclude []string `short:"E" env:"MYSCHEMA_EXCLUDE" help:"Exclude tables matching the pattern."`
}

// MatchName tells whether name passes the include/exclude filters.
func (f *FilterOptions) MatchName(name string) bool {
	if len(f.Include) > 0 {
		matched := false
		for _, p := range f.Include {
			if ok, _ := path.Match(p, name); ok {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	for _, p := range f.Exclude {
		if ok, _ := path.Match(p, name); ok {
			return false
		}
	}
	return true
}

// AfterApply is called by kong; validates the patterns once.
func (f *FilterOptions) AfterApply() error {
	for _, p := range f.Include {
		if _, err := path.Match(p, ""); err != nil {
			return fmt.Errorf("invalid --include pattern %q: %w", p, err)
		}
	}
	for _, p := range f.Exclude {
		if _, err := path.Match(p, ""); err != nil {
			return fmt.Errorf("invalid --exclude pattern %q: %w", p, err)
		}
	}
	return nil
}

// AlterOption carries the MySQL online-DDL hints that get appended to
// every generated `ALTER TABLE …` and `CREATE INDEX …` statement.
// myschema absorbs the syntax mismatch between the two DDLs (ALTER
// TABLE wants `, ALGORITHM=…, LOCK=…`; CREATE INDEX wants the same
// clauses separated by spaces with no leading comma) so the user
// supplies just the values. myschema does not validate which
// algorithm / lock combination MySQL will accept for a given change —
// apply lets MySQL reject unsupported combinations at execution time.
type AlterOption struct {
	AlterAlgorithm string `env:"MYSCHEMA_ALTER_ALGORITHM" enum:",DEFAULT,INSTANT,INPLACE,COPY" default:"" help:"ALGORITHM= clause appended to every generated ALTER TABLE / CREATE INDEX. One of DEFAULT, INSTANT, INPLACE, COPY (MySQL 8.0+)."`
	AlterLock      string `env:"MYSCHEMA_ALTER_LOCK" enum:",DEFAULT,NONE,SHARED,EXCLUSIVE" default:"" help:"LOCK= clause appended to every generated ALTER TABLE / CREATE INDEX. One of DEFAULT, NONE, SHARED, EXCLUSIVE."`
	// BulkAlter folds consecutive same-table ALTER TABLE statements
	// into one multi-spec ALTER. FK ops, partition ops, RENAME COLUMN /
	// INDEX, and standalone CREATE INDEX are excluded — see CAVEATS.md
	// for the rationale.
	BulkAlter bool `env:"MYSCHEMA_BULK_ALTER" help:"Combine consecutive same-table ALTER TABLE statements into one multi-spec ALTER. FK / partition / RENAME COLUMN / RENAME INDEX / standalone CREATE INDEX excluded."`
}

// PreSQLOption carries a one-shot SQL payload that runs against the
// connection right after connect() and before plan / apply does any
// real work. Typical use: session-level SET statements that change
// how the server interprets the upcoming DDL or catalog reads
// (e.g. `SET FOREIGN_KEY_CHECKS=0`, `SET sql_mode='TRADITIONAL'`,
// `SET explicit_defaults_for_timestamp=ON`). Only one source can be
// set at a time — `PreSQL` and `PreSQLFile` are mutually exclusive
// to keep the precedence rule trivial. Multi-statement payloads are
// supported (split on `;`); each statement is executed sequentially
// against the same connection. dump does NOT run pre-SQL — it's
// read-only and pre-SQL's typical use is DDL session setup.
type PreSQLOption struct {
	// kong's `xor` group rejects the both-set case at parse time
	// before our code sees it. The runtime check in loadPreSQL
	// stays in place to cover programmatic API callers (Apply /
	// Plan invoked from Go without going through kong).
	PreSQL     string `xor:"pre-sql" env:"MYSCHEMA_PRE_SQL" help:"SQL statement(s) to run on the connection before plan / apply (typically session SETs). Multi-statement input is split on ';'. Mutually exclusive with --pre-sql-file."`
	PreSQLFile string `xor:"pre-sql" env:"MYSCHEMA_PRE_SQL_FILE" help:"Path to a file with SQL statement(s) to run on the connection before plan / apply. Mutually exclusive with --pre-sql. stdin (-) is not supported; use --pre-sql for inline payload."`
}

// DropPolicy decides which DROP categories the diff is allowed to emit.
type DropPolicy struct {
	AllowDrop []string `env:"MYSCHEMA_ALLOW_DROP" enum:"all,table,view,column,constraint,foreign_key,index,partition" help:"Comma-separated drop categories to allow."`
}

// IsDropAllowed reports whether kind is in the allow list. The "all" wildcard
// permits every kind.
func (p *DropPolicy) IsDropAllowed(kind string) bool {
	for _, k := range p.AllowDrop {
		if k == "all" || k == kind {
			return true
		}
	}
	return false
}

// ObjectCount summarises how many objects were considered during a run.
type ObjectCount struct {
	Database string
	Tables   int
	Views    int
}

func (c ObjectCount) Summary() string {
	return fmt.Sprintf("%d table(s), %d view(s)", c.Tables, c.Views)
}

func (c ObjectCount) DBLabel() string {
	return "database " + c.Database
}
