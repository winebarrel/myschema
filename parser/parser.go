// Package parser turns desired-schema SQL into the model.* representation.
// Built on vitess.io/vitess/go/vt/sqlparser; vitess covers more MySQL syntax
// than pingcap (notably the SPATIAL types) and produces a smaller binary.
package parser

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"vitess.io/vitess/go/vt/sqlparser"

	"github.com/winebarrel/myschema/model"
	"github.com/winebarrel/orderedmap"
)

// ParseResult holds everything the parser produces.
type ParseResult struct {
	Tables *orderedmap.Map[string, *model.Table]
	Views  *orderedmap.Map[string, *model.View]
	// Executes are the `-- myschema:execute <check-sql>` blocks in
	// desired-side order. plan / apply iterate them after every other
	// table / view DDL so triggers / routines / events created via the
	// directive can refer to brand-new tables.
	Executes []*model.ExecuteGroup
}

// readSQLFile reads a SQL file (or "-" for stdin).
func readSQLFile(path string) (string, error) {
	var (
		data []byte
		err  error
	)
	if path == "-" {
		data, err = io.ReadAll(os.Stdin)
	} else {
		data, err = os.ReadFile(path)
	}
	if err != nil {
		return "", fmt.Errorf("failed to read SQL file %s: %w", path, err)
	}
	return string(data), nil
}

// ParseSQLFiles parses one or more SQL files using defaultDB as the database
// for any unqualified table name.
func ParseSQLFiles(paths []string, defaultDB string) (*ParseResult, error) {
	var sqls []string
	for _, p := range paths {
		s, err := readSQLFile(p)
		if err != nil {
			return nil, err
		}
		sqls = append(sqls, s)
	}
	return ParseSQL(strings.Join(sqls, "\n"), defaultDB)
}

func newParser() (*sqlparser.Parser, error) {
	return sqlparser.New(sqlparser.Options{})
}

// ParseSQL parses a SQL string into a ParseResult.
func ParseSQL(sql, defaultDB string) (*ParseResult, error) {
	if err := validateDirectives(sql); err != nil {
		return nil, err
	}
	p, err := newParser()
	if err != nil {
		return nil, fmt.Errorf("failed to create parser: %w", err)
	}
	pieces, err := p.SplitStatementToPieces(sql)
	if err != nil {
		return nil, fmt.Errorf("failed to split SQL: %w", err)
	}

	tables := orderedmap.New[string, *model.Table]()
	views := orderedmap.New[string, *model.View]()
	var executes []*model.ExecuteGroup

	for _, piece := range pieces {
		piece = strings.TrimSpace(piece)
		if piece == "" {
			continue
		}
		// `-- myschema:execute <check-sql>` short-circuits the vitess
		// pass: the typical payload (CREATE TRIGGER, CREATE PROCEDURE,
		// …) lives outside vitess's grammar, so the parser hands the
		// remainder of the piece to the model as raw SQL and lets
		// plan / apply talk to MySQL directly. Done before the rename
		// extractors so an `execute` block doesn't waste cycles
		// looking for renames inside an opaque payload.
		checkSQL, executeSQL, ok, err := extractExecuteDirective(piece)
		if err != nil {
			return nil, err
		}
		if ok {
			// `-- myschema:execute` and `-- myschema:renamed-from` both
			// describe how to act on the *next* statement, so stacking
			// them is ambiguous. Reject upfront instead of silently
			// dropping one — the rename branch wouldn't run anyway
			// because we short-circuit the vitess parse below.
			if stmtRename, rErr := extractStmtRenameFrom(piece); rErr != nil {
				return nil, rErr
			} else if stmtRename != "" {
				return nil, fmt.Errorf("-- myschema:execute %q and -- myschema:renamed-from %q in the same statement: directives cannot be combined", checkSQL, stmtRename)
			}
			if inline := extractInlineRenames(piece); len(inline.Columns)+len(inline.Indexes)+len(inline.Constraints)+len(inline.ForeignKeys)+len(inline.Unsupported) > 0 {
				return nil, fmt.Errorf("-- myschema:execute %q: -- myschema:renamed-from cannot appear inside an execute payload (the payload is held as raw SQL and never parsed)", checkSQL)
			}
			if cc, ccErr := extractStmtConvertCharset(piece); ccErr != nil {
				return nil, fmt.Errorf("-- myschema:execute %q: %w", checkSQL, ccErr)
			} else if cc {
				return nil, fmt.Errorf("-- myschema:execute %q and -- myschema:convert-charset in the same statement: directives cannot be combined", checkSQL)
			}
			// Validate the check SQL is read-only: it runs on every
			// plan / apply, so accidentally writing DDL/DML here would
			// surprise the user. SELECT / WITH / UNION are the safe
			// shapes; anything else (INSERT, UPDATE, DDL, multi-stmt)
			// must error here so the mistake surfaces at parse time
			// rather than at first plan run.
			if cErr := validateExecuteCheckSQL(p, checkSQL); cErr != nil {
				return nil, fmt.Errorf("-- myschema:execute %q: %w", checkSQL, cErr)
			}
			executeSQL = strings.TrimSpace(executeSQL)
			// "Empty" here covers two shapes: a literally empty
			// remainder, and a remainder whose lines are all blank /
			// `--` / `#` / `/* … */`. Without the comment-aware
			// check the latter slips through as a non-empty payload
			// and MySQL returns "Query was empty" at apply time.
			if executeSQL == "" || payloadHasNoSQL(executeSQL) {
				return nil, fmt.Errorf("-- myschema:execute %q: missing the SQL statement that the directive guards (write the SQL on the line(s) after the directive)", checkSQL)
			}
			// SplitStatementToPieces strips the trailing `;`. Re-add
			// it so plan output looks like every other emitted
			// statement and apply hands MySQL a syntactically clean
			// piece either way.
			if !strings.HasSuffix(executeSQL, ";") {
				executeSQL += ";"
			}
			executes = append(executes, &model.ExecuteGroup{
				CheckSQL:   checkSQL,
				ExecuteSQL: executeSQL,
			})
			continue
		}
		// Directive extraction runs against the raw piece before vitess
		// strips comments. Statement-level directives sit on a leading
		// comment block; inline directives (column / index renames) live
		// inside the CREATE TABLE body, classified per-kind so a column
		// and an index that share a name (which happens when a KEY is
		// auto-named after its first column) don't compete for the same
		// directive.
		stmtRename, err := extractStmtRenameFrom(piece)
		if err != nil {
			return nil, err
		}
		stmtConvertCharset, err := extractStmtConvertCharset(piece)
		if err != nil {
			return nil, err
		}
		inline := extractInlineRenames(piece)
		// ParseStrictDDL (vs Parse) bypasses vitess's partial-DDL
		// fallback. Parse2 — which Parse calls under the hood —
		// swallows syntax errors as a WARN log and returns the
		// partially-parsed AST with err=nil when vitess's tokenizer
		// has a `partialDDL` to fall back to. That fallback would
		// let inputs like `id INT STORAGE DISK` (NDB-only attribute,
		// no InnoDB grammar) through silently, and myschema would
		// emit an empty `CREATE TABLE t ();` from the half-built AST.
		// ParseStrictDDL skips the fallback and returns the tokenizer
		// LastError instead, so the user sees the actual parse error
		// at plan time rather than a half-applied schema at apply
		// time.
		stmt, err := p.ParseStrictDDL(piece)
		if err != nil {
			return nil, fmt.Errorf("failed to parse SQL: %w", err)
		}
		switch s := stmt.(type) {
		case *sqlparser.CreateTable:
			t, err := parseCreateTable(s, defaultDB)
			if err != nil {
				return nil, err
			}
			if stmtRename != "" {
				rn := stmtRename
				t.RenameFrom = &rn
			}
			if stmtConvertCharset {
				// `-- myschema:renamed-from` and `-- myschema:convert-charset`
				// both attach to the same CREATE TABLE but describe
				// fundamentally different operations (RENAME vs.
				// CONVERT TO), and the diff layer doesn't combine
				// them into one statement. Reject upfront — same
				// posture as the execute / renamed-from conflict
				// upstream.
				if stmtRename != "" {
					return nil, fmt.Errorf("table %s: -- myschema:convert-charset and -- myschema:renamed-from %q in the same statement: directives cannot be combined", t.FQTN(), stmtRename)
				}
				if t.Charset == nil {
					return nil, fmt.Errorf("table %s: -- myschema:convert-charset requires the CREATE TABLE to declare a DEFAULT CHARSET (no charset on the table → nothing to CONVERT TO)", t.FQTN())
				}
				t.ConvertCharset = true
			}
			for name, old := range inline.Columns {
				c, ok := t.Columns.GetOk(name)
				if !ok {
					return nil, fmt.Errorf("table %s: -- myschema:renamed-from %s: target column %q not found in CREATE TABLE", t.FQTN(), old, name)
				}
				o := old
				c.RenameFrom = &o
			}
			for name, old := range inline.Indexes {
				idx, ok := t.Indexes.GetOk(name)
				if !ok {
					return nil, fmt.Errorf("table %s: -- myschema:renamed-from %s: target index %q not found in CREATE TABLE", t.FQTN(), old, name)
				}
				o := old
				idx.RenameFrom = &o
			}
			for name, old := range inline.Constraints {
				con, ok := t.Constraints.GetOk(name)
				if !ok {
					return nil, fmt.Errorf("table %s: -- myschema:renamed-from %s: target constraint %q not found in CREATE TABLE", t.FQTN(), old, name)
				}
				o := old
				con.RenameFrom = &o
			}
			for name, old := range inline.ForeignKeys {
				fk, ok := t.ForeignKeys.GetOk(name)
				if !ok {
					return nil, fmt.Errorf("table %s: -- myschema:renamed-from %s: target foreign key %q not found in CREATE TABLE", t.FQTN(), old, name)
				}
				o := old
				fk.RenameFrom = &o
			}
			if len(inline.Unsupported) > 0 {
				u := inline.Unsupported[0]
				return nil, fmt.Errorf("table %s: -- myschema:renamed-from %s: %s", t.FQTN(), u.OldName, u.Reason)
			}
			if _, dup := tables.GetOk(t.FQTN()); dup {
				return nil, fmt.Errorf("duplicate table: %s", t.FQTN())
			}
			tables.Set(t.FQTN(), t)

		case *sqlparser.AlterTable:
			if err := applyAlterTable(tables, s, defaultDB); err != nil {
				return nil, err
			}
			if err := rejectMisplacedRenameDirectives(stmtRename, inline, "ALTER TABLE"); err != nil {
				return nil, err
			}
			if err := rejectMisplacedConvertCharset(stmtConvertCharset, "ALTER TABLE"); err != nil {
				return nil, err
			}

		case *sqlparser.CreateView:
			v, err := parseCreateView(s, defaultDB)
			if err != nil {
				return nil, err
			}
			if _, dup := views.GetOk(v.FQVN()); dup {
				return nil, fmt.Errorf("duplicate view: %s", v.FQVN())
			}
			views.Set(v.FQVN(), v)
			if err := rejectMisplacedRenameDirectives(stmtRename, inline, "CREATE VIEW"); err != nil {
				return nil, err
			}
			if err := rejectMisplacedConvertCharset(stmtConvertCharset, "CREATE VIEW"); err != nil {
				return nil, err
			}

		default:
			// Skip statements we don't model (CREATE DATABASE, COMMENT, SET,
			// GRANT, etc).
			kind := fmt.Sprintf("unsupported statement (%T)", s)
			if err := rejectMisplacedRenameDirectives(stmtRename, inline, kind); err != nil {
				return nil, err
			}
			if err := rejectMisplacedConvertCharset(stmtConvertCharset, kind); err != nil {
				return nil, err
			}
		}
	}

	return &ParseResult{Tables: tables, Views: views, Executes: executes}, nil
}

// validateExecuteCheckSQL parses checkSQL through vitess and ensures it
// is a single read-only statement (Select / Union — vitess folds WITH
// into the Select shape). Anything else (INSERT / UPDATE / DDL /
// multi-statement) errors so a check that would mutate the database
// during plan / apply is caught at parse time rather than discovered
// when it runs.
func validateExecuteCheckSQL(p *sqlparser.Parser, checkSQL string) error {
	pieces, err := p.SplitStatementToPieces(checkSQL)
	if err != nil {
		return fmt.Errorf("check SQL does not parse: %w", err)
	}
	// More than one piece means an internal `;` separating statements,
	// which would let a SELECT smuggle a follow-up DDL/DML past the
	// type check below.
	nonEmpty := 0
	var only string
	for _, piece := range pieces {
		if strings.TrimSpace(piece) == "" {
			continue
		}
		nonEmpty++
		only = piece
	}
	if nonEmpty != 1 {
		return fmt.Errorf("check SQL must be exactly one statement (got %d)", nonEmpty)
	}
	stmt, err := p.Parse(only)
	if err != nil {
		return fmt.Errorf("check SQL does not parse: %w", err)
	}
	switch stmt.(type) {
	case *sqlparser.Select, *sqlparser.Union:
		return nil
	default:
		return fmt.Errorf("check SQL must be a SELECT (or UNION / WITH … SELECT); got %T", stmt)
	}
}

// rejectMisplacedConvertCharset errors if a convert-charset directive
// was extracted from a statement that isn't CREATE TABLE. The directive
// only makes sense on a desired-side CREATE TABLE — the catalog has no
// representation for it, and the diff layer only consults
// model.Table.ConvertCharset.
func rejectMisplacedConvertCharset(found bool, stmtKind string) error {
	if found {
		return fmt.Errorf("-- myschema:convert-charset: directive is only supported on CREATE TABLE, not %s", stmtKind)
	}
	return nil
}

// rejectMisplacedRenameDirectives errors if any rename directives were
// extracted from a statement we don't currently support directives on.
// Only `CREATE TABLE` participates today; anywhere else, a directive
// would be silently dropped on the floor and the corresponding rename
// would degrade into a destructive DROP+CREATE on the next plan.
func rejectMisplacedRenameDirectives(stmtRename string, inline *inlineRenames, stmtKind string) error {
	switch {
	case stmtRename != "":
		return fmt.Errorf("-- myschema:renamed-from %s: directive is only supported on CREATE TABLE, not %s", stmtRename, stmtKind)
	case len(inline.Columns) > 0,
		len(inline.Indexes) > 0,
		len(inline.Constraints) > 0,
		len(inline.ForeignKeys) > 0,
		len(inline.Unsupported) > 0:
		return fmt.Errorf("-- myschema:renamed-from: directive is only supported inside CREATE TABLE, not %s", stmtKind)
	}
	return nil
}

func dbName(schema, defaultDB string) string {
	if schema != "" {
		return schema
	}
	return defaultDB
}

// resolveStmtDB returns the database the statement targets, erroring
// when a desired-side qualifier disagrees with the invocation database.
// myschema emits unqualified DDL (CLAUDE.md "Operational rules") and
// runs against exactly one database per invocation, so a desired-side
// `CREATE TABLE other_db.users (...)` would silently apply to the
// connected database, not other_db. Reject upfront with a message that
// names both databases so the user can either drop the qualifier or
// re-point the DSN. defaultDB is empty only in narrow test paths that
// pre-date the constraint; the validation is skipped there to keep
// existing fixtures working.
func resolveStmtDB(qualifier, defaultDB, kind, name string) (string, error) {
	if qualifier == "" || defaultDB == "" || qualifier == defaultDB {
		return dbName(qualifier, defaultDB), nil
	}
	return "", fmt.Errorf("%s %s.%s: cross-database reference not supported (myschema operates on database %q only — remove the qualifier or run myschema against %q)", kind, qualifier, name, defaultDB, qualifier)
}

func parseCreateTable(s *sqlparser.CreateTable, defaultDB string) (*model.Table, error) {
	db, err := resolveStmtDB(s.Table.Qualifier.String(), defaultDB, "CREATE TABLE", s.Table.Name.String())
	if err != nil {
		return nil, err
	}
	t := &model.Table{
		Database:    db,
		Name:        s.Table.Name.String(),
		Columns:     orderedmap.New[string, *model.Column](),
		Constraints: orderedmap.New[string, *model.Constraint](),
		ForeignKeys: orderedmap.New[string, *model.ForeignKey](),
		Indexes:     orderedmap.New[string, *model.Index](),
	}
	if s.TableSpec == nil {
		return t, nil
	}

	for _, cd := range s.TableSpec.Columns {
		c, err := parseColumnDef(cd)
		if err != nil {
			return nil, fmt.Errorf("table %s: column %s: %w", t.FQTN(), cd.Name.String(), err)
		}
		if _, dup := t.Columns.GetOk(c.Name); dup {
			return nil, fmt.Errorf("duplicate column %s.%s", t.FQTN(), c.Name)
		}
		t.Columns.Set(c.Name, c)

		// Inline column-level PRIMARY KEY / UNIQUE
		// (`col INT PRIMARY KEY`, `col VARCHAR(64) UNIQUE`).
		// vitess parses these as a column attribute (KeyOpt) rather
		// than promoting them to a TableSpec.Indexes entry, so the
		// table-level index loop below would never see them. Mirror
		// the table-level behaviour from `addIndex`'s IndexTypePrimary
		// / IndexTypeUnique cases here so the inline form round-trips
		// cleanly. Bare column-level `KEY` is treated as a PRIMARY
		// KEY synonym to match MySQL's behaviour; see
		// applyInlineColumnKey's doc for the full enum mapping
		// (including which vitess enum values are unreachable from
		// valid SQL).
		if cd.Type.Options != nil {
			if err := applyInlineColumnKey(t, c.Name, cd.Type.Options.KeyOpt); err != nil {
				return nil, err
			}
		}
	}

	for _, idx := range s.TableSpec.Indexes {
		if err := addIndex(t, idx); err != nil {
			return nil, err
		}
	}

	for _, c := range s.TableSpec.Constraints {
		if err := addTableConstraint(t, c, defaultDB); err != nil {
			return nil, err
		}
	}

	for _, opt := range s.TableSpec.Options {
		applyTableOption(t, opt)
	}
	if s.TableSpec.PartitionOption != nil {
		if s.TableSpec.PartitionOption.SubPartition != nil {
			return nil, fmt.Errorf("table %s: SUBPARTITION BY … is out of scope for v1 partition support; remove the SUBPARTITION clause or manage the table outside myschema", t.FQTN())
		}
		p := NormalizePartitionOption(s.TableSpec.PartitionOption)
		t.Partition = &p
	}
	// Drop a redundantly-spelled COLLATE: `CHARSET=utf8mb4
	// COLLATE=utf8mb4_0900_ai_ci` and bare `CHARSET=utf8mb4` describe
	// the same MySQL state. Normalising to nil here keeps the parser
	// side aligned with the catalog side, which collapses the same
	// case after reading information_schema.
	t.Collation = model.CollapseDefaultCollation(effectiveCharsetForCollation(t.Charset, t.Collation), t.Collation)
	for _, c := range t.Columns.CollectValues() {
		// Effective charset for collapsing: column's own CHARACTER SET
		// if given, else the table default, else the charset implied
		// by the column's COLLATE name. The last fallback covers the
		// COLLATE-only column on a COLLATE-only table, where neither
		// declared a CHARACTER SET; without it a redundant default
		// collation would survive on the parser side while the catalog
		// side (which always resolves the effective charset via
		// information_schema) collapses it — endless MODIFY COLUMN
		// drift.
		effectiveCharset := c.CharacterSet
		if effectiveCharset == nil {
			effectiveCharset = t.Charset
		}
		effectiveCharset = effectiveCharsetForCollation(effectiveCharset, c.Collation)
		c.Collation = model.CollapseDefaultCollation(effectiveCharset, c.Collation)
	}

	return t, nil
}

func parseColumnDef(cd *sqlparser.ColumnDefinition) (*model.Column, error) {
	c := &model.Column{
		Name:     cd.Name.String(),
		TypeName: columnTypeString(cd.Type),
	}
	if cd.Type.Charset.Name != "" {
		// vitess preserves the user's original casing; the catalog
		// always reports lower-case in information_schema.COLUMNS, so
		// normalise here to keep the two sides comparable.
		cs := strings.ToLower(cd.Type.Charset.Name)
		c.CharacterSet = &cs
	}
	opts := cd.Type.Options
	if opts == nil {
		return c, nil
	}

	// MySQL parses but ignores inline column-level REFERENCES — see
	// https://dev.mysql.com/doc/refman/8.0/en/ansi-diff-foreign-keys.html
	// which calls the clause "a memo or comment". A previous version
	// of myschema rescued it by promoting to an explicit-named
	// ALTER TABLE ADD CONSTRAINT, but that diverged from MySQL's
	// semantics: the user thought they'd written a working FK, and
	// the rescue's auto-name (`<table>_ibfk_<col>`) drifted against
	// MySQL's auto-name (`<table>_ibfk_<n>`) for any catalog state
	// created outside myschema. Reject the shape outright so the
	// desired SQL spells out the table-level form the user actually
	// wants.
	if opts.Reference != nil {
		return nil, fmt.Errorf("inline column-level REFERENCES is silently ignored by MySQL (see https://dev.mysql.com/doc/refman/8.0/en/ansi-diff-foreign-keys.html); declare the FK as a table-level `[CONSTRAINT name] FOREIGN KEY (col) REFERENCES other(col)` clause instead")
	}

	if opts.Null != nil && !*opts.Null {
		c.NotNull = true
	}
	if opts.Default != nil {
		// vitess hands back *NullVal for an explicit `DEFAULT NULL`,
		// but catalog/loadColumns leaves Default=nil for SQL-NULL
		// COLUMN_DEFAULT (which is what MySQL stores for any nullable
		// column, with or without an explicit DEFAULT NULL clause).
		// Without this skip, parser-side `&"null"` would never compare
		// equal to catalog-side `nil` and `MODIFY COLUMN ts timestamp
		// DEFAULT null` would re-fire on every plan — the canonical
		// `TIMESTAMP NULL DEFAULT NULL` idiom (Rails/Laravel/Django
		// soft-delete columns) would drift forever.
		if _, isNull := opts.Default.(*sqlparser.NullVal); !isNull {
			v := normalizeDefaultExpr(sqlparser.String(opts.Default))
			c.Default = &v
		}
	}
	if opts.OnUpdate != nil {
		v := normalizeDefaultExpr(sqlparser.String(opts.OnUpdate))
		c.OnUpdate = &v
	}
	if opts.Autoincrement {
		c.AutoIncrement = true
	}
	if opts.Comment != nil {
		// Literal.Val is the unquoted, escape-resolved string value;
		// sqlparser.String would re-add the wrapping quotes.
		raw := opts.Comment.Val
		c.Comment = &raw
	}
	if opts.Collate != "" {
		// Lowercase to match information_schema.COLUMNS, which always
		// reports collations in canonical lower-case.
		coll := strings.ToLower(opts.Collate)
		c.Collation = &coll
	}
	// vitess uses `Invisible *bool` (nil = unspecified, false = VISIBLE,
	// true = INVISIBLE). Only the true case is interesting to the model:
	// VISIBLE is MySQL's default, so a nil / false stays Column.Invisible
	// = false and emits nothing on round-trip.
	if opts.Invisible != nil && *opts.Invisible {
		c.Invisible = true
	}
	if opts.As != nil {
		expr := sqlparser.String(opts.As)
		c.Generated = &expr
		c.Stored = opts.Storage == sqlparser.StoredStorage
	}
	// SRID on spatial columns (`GEOMETRY ... SRID 4326`). vitess parks
	// the value as a *Literal whose Val is the integer text. SRID 0 is
	// a valid explicit declaration (distinct from "no SRID"), so we
	// preserve a *uint32 zero rather than nil.
	if opts.SRID != nil {
		n, err := strconv.ParseUint(opts.SRID.Val, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("SRID %q is not a valid uint32: %w", opts.SRID.Val, err)
		}
		v := uint32(n)
		c.SRID = &v
	}

	return c, nil
}

// columnTypeString returns just the column type the catalog also produces
// (e.g. "bigint", "varchar(255)", "decimal(10,2)", "geometry"). vitess's
// Format(*ColumnType) inlines both Options (NOT NULL, AUTO_INCREMENT …)
// AND the column-level CHARACTER SET / COLLATE alongside the type, neither
// of which the catalog returns inside COLUMN_TYPE. We blank both fields
// for the duration of the format call and let vitess handle the rest —
// including ENUM/SET value lists, (length), (length,scale), UNSIGNED and
// ZEROFILL. ENUM/SET come out with `'a', 'b'` (extra space); we squeeze
// that down to match information_schema's `'a','b'`.
func columnTypeString(t *sqlparser.ColumnType) string {
	savedOpts, savedCharset := t.Options, t.Charset
	t.Options = nil
	t.Charset = sqlparser.ColumnCharset{}
	defer func() { t.Options, t.Charset = savedOpts, savedCharset }()

	s := strings.ToLower(sqlparser.String(t))
	if strings.HasPrefix(s, "enum(") || strings.HasPrefix(s, "set(") {
		s = strings.ReplaceAll(s, ", ", ",")
	}
	return s
}

func addIndex(t *model.Table, idx *sqlparser.IndexDefinition) error {
	parts := make([]model.IndexPart, 0, len(idx.Columns))
	cols := make([]string, 0, len(idx.Columns))
	for _, c := range idx.Columns {
		p := model.IndexPart{}
		if c.Expression != nil {
			p.Expr = sqlparser.String(c.Expression)
		} else {
			p.Column = c.Column.String()
			cols = append(cols, p.Column)
		}
		if c.Length != nil {
			p.Length = *c.Length
		}
		if c.Direction == sqlparser.DescOrder {
			p.Desc = true
		}
		parts = append(parts, p)
	}

	indexType := ""
	invisible := false
	var comment *string
	for _, opt := range idx.Options {
		switch strings.ToLower(opt.Name) {
		case "using":
			indexType = strings.ToUpper(opt.String)
		case "invisible":
			invisible = true
		case "comment":
			// vitess parks the unquoted comment value in
			// opt.Value.Val (a *Literal); opt.String holds
			// `using`'s payload but is empty here.
			//
			// An explicit `COMMENT ''` is folded to nil to match
			// the catalog reader's normalisation of empty
			// information_schema.STATISTICS.INDEX_COMMENT — without
			// the fold, parser-side `&""` would never compare equal
			// to catalog-side `nil` and `indexEqual` would fire
			// DROP+CREATE on every plan.
			if opt.Value != nil && opt.Value.Val != "" {
				v := opt.Value.Val
				comment = &v
			}
		}
	}

	switch idx.Info.Type {
	case sqlparser.IndexTypePrimary:
		def, _ := indexBodySQLFromParts(parts)
		t.Constraints.Set("PRIMARY", &model.Constraint{
			Name:       "PRIMARY",
			Type:       model.PrimaryKeyConstraint,
			Definition: def,
			Columns:    cols,
		})
		t.Indexes.Set("PRIMARY", &model.Index{
			Name: "PRIMARY", Database: t.Database, Table: t.Name,
			Primary: true, Parts: parts,
		})
		for _, name := range cols {
			if col, ok := t.Columns.GetOk(name); ok {
				col.NotNull = true
			}
		}
		return nil

	case sqlparser.IndexTypeUnique:
		name := idx.Info.Name.String()
		if name == "" && len(cols) > 0 {
			name = cols[0]
		}
		i := &model.Index{
			Name: name, Database: t.Database, Table: t.Name,
			KeyType: model.IndexUnique, Parts: parts,
			IndexType: indexType, Invisible: invisible,
			Comment: comment,
		}
		if _, dup := t.Indexes.GetOk(name); dup {
			return fmt.Errorf("duplicate index: %s on %s", name, t.FQTN())
		}
		t.Indexes.Set(name, i)
		return nil

	case sqlparser.IndexTypeFullText:
		name := idx.Info.Name.String()
		if name == "" && len(cols) > 0 {
			name = cols[0]
		}
		if _, dup := t.Indexes.GetOk(name); dup {
			return fmt.Errorf("duplicate index: %s on %s", name, t.FQTN())
		}
		t.Indexes.Set(name, &model.Index{
			Name: name, Database: t.Database, Table: t.Name,
			KeyType: model.IndexFulltext, Parts: parts, Invisible: invisible,
			Comment: comment,
		})
		return nil

	case sqlparser.IndexTypeSpatial:
		name := idx.Info.Name.String()
		if name == "" && len(cols) > 0 {
			name = cols[0]
		}
		if _, dup := t.Indexes.GetOk(name); dup {
			return fmt.Errorf("duplicate index: %s on %s", name, t.FQTN())
		}
		t.Indexes.Set(name, &model.Index{
			Name: name, Database: t.Database, Table: t.Name,
			KeyType: model.IndexSpatial, Parts: parts, Invisible: invisible,
			Comment: comment,
		})
		return nil

	default: // IndexTypeDefault — KEY / INDEX
		name := idx.Info.Name.String()
		if name == "" && len(cols) > 0 {
			name = cols[0]
		}
		i := &model.Index{
			Name: name, Database: t.Database, Table: t.Name,
			Parts: parts, IndexType: indexType, Invisible: invisible,
			Comment: comment,
		}
		if _, dup := t.Indexes.GetOk(name); dup {
			return fmt.Errorf("duplicate index: %s on %s", name, t.FQTN())
		}
		t.Indexes.Set(name, i)
		return nil
	}
}

func indexBodySQLFromParts(parts []model.IndexPart) (string, []string) {
	var b strings.Builder
	cols := make([]string, 0, len(parts))
	b.WriteString("(")
	for i, p := range parts {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(p.SQL())
		if p.Expr == "" {
			cols = append(cols, p.Column)
		}
	}
	b.WriteString(")")
	return b.String(), cols
}

func addTableConstraint(t *model.Table, c *sqlparser.ConstraintDefinition, defaultDB string) error {
	switch d := c.Details.(type) {
	case *sqlparser.ForeignKeyDefinition:
		cols := make([]string, 0, len(d.Source))
		for _, col := range d.Source {
			cols = append(cols, col.String())
		}
		fk, err := buildFK(c.Name.String(), t, cols, d.ReferenceDefinition, defaultDB)
		if err != nil {
			return err
		}
		if fk.Name == "" && len(cols) > 0 {
			fk.Name = autoFKName(t.Name, cols[0])
		}
		if _, dup := t.ForeignKeys.GetOk(fk.Name); dup {
			return fmt.Errorf("duplicate foreign key: %s on %s", fk.Name, t.FQTN())
		}
		t.ForeignKeys.Set(fk.Name, fk)
		return nil

	case *sqlparser.CheckConstraintDefinition:
		expr := sqlparser.String(d.Expr)
		name := c.Name.String()
		if name == "" {
			name = autoCheckName(t.Name, t.Constraints.Len())
		}
		def := "CHECK (" + expr + ")"
		t.Constraints.Set(name, &model.Constraint{
			Name:       name,
			Type:       model.CheckConstraint,
			Definition: def,
			Enforced:   d.Enforced,
		})
		return nil
	}
	return nil
}

func buildFK(name string, t *model.Table, cols []string, ref *sqlparser.ReferenceDefinition, defaultDB string) (*model.ForeignKey, error) {
	if ref == nil {
		return nil, fmt.Errorf("foreign key %s on %s: missing REFERENCES target", name, t.FQTN())
	}
	refCols := make([]string, 0, len(ref.ReferencedColumns))
	for _, c := range ref.ReferencedColumns {
		refCols = append(refCols, c.String())
	}
	fk := &model.ForeignKey{
		Name:     name,
		Database: t.Database, Table: t.Name,
		Columns:  cols,
		RefDB:    dbName(ref.ReferencedTable.Qualifier.String(), defaultDB),
		RefTable: ref.ReferencedTable.Name.String(),
		RefCols:  refCols,
	}
	fk.OnDelete = referenceActionString(ref.OnDelete)
	fk.OnUpdate = referenceActionString(ref.OnUpdate)
	switch ref.Match {
	case sqlparser.Full:
		fk.MatchType = "FULL"
	case sqlparser.Partial:
		fk.MatchType = "PARTIAL"
	case sqlparser.Simple:
		fk.MatchType = "SIMPLE"
	}
	return fk, nil
}

// referenceActionString maps vitess's ReferenceAction onto the strings the
// catalog reader produces. RESTRICT and NO ACTION are MySQL semantic
// equivalents and information_schema.REFERENTIAL_CONSTRAINTS reports both
// as the empty (default) action — so we collapse them too. Otherwise an
// `ON DELETE NO ACTION` in desired SQL would diff against a catalog row
// whose UPDATE_RULE / DELETE_RULE was canonicalised to nothing.
func referenceActionString(a sqlparser.ReferenceAction) string {
	switch a {
	case sqlparser.Cascade:
		return "CASCADE"
	case sqlparser.SetNull:
		return "SET NULL"
	case sqlparser.SetDefault:
		return "SET DEFAULT"
	case sqlparser.NoAction, sqlparser.Restrict:
		return ""
	}
	return ""
}

// effectiveCharsetForCollation returns charset unchanged when it's
// already set; otherwise it derives a charset from the collation name
// (e.g. utf8mb4_0900_ai_ci → utf8mb4) so that CollapseDefaultCollation
// can still fire for COLLATE-only desired SQL. Returns nil unchanged
// if there's nothing to derive from on either side.
func effectiveCharsetForCollation(charset, collation *string) *string {
	if charset != nil || collation == nil {
		return charset
	}
	cs := model.CharsetOfCollation(*collation)
	if cs == "" {
		return nil
	}
	return &cs
}

func applyTableOption(t *model.Table, opt *sqlparser.TableOption) {
	switch strings.ToUpper(opt.Name) {
	case "ENGINE":
		v := opt.String
		t.Engine = &v
	case "CHARSET", "CHARACTER SET", "DEFAULT CHARSET", "DEFAULT CHARACTER SET":
		// MySQL identifiers for charsets and collations are case-
		// insensitive; information_schema always reports the canonical
		// lower-case form, so normalise here so parser-side and
		// catalog-side values compare equal regardless of how the user
		// spelled them in the desired SQL.
		v := strings.ToLower(opt.String)
		t.Charset = &v
	case "COLLATE", "DEFAULT COLLATE":
		v := strings.ToLower(opt.String)
		t.Collation = &v
	case "COMMENT":
		if opt.Value != nil {
			// Literal.Val is the unquoted, escape-resolved value.
			v := opt.Value.Val
			t.Comment = &v
		}
	case "AUTO_INCREMENT":
		if opt.Value != nil {
			if n, err := strconv.ParseUint(opt.Value.Val, 10, 64); err == nil {
				t.AutoIncrement = &n
			}
		}
	}
}

// normalizeDefaultExpr re-shapes vitess's restored default-value SQL so
// that it matches what information_schema.COLUMNS.COLUMN_DEFAULT (and
// EXTRA for ON UPDATE) hands back. The two systematic differences:
//   - vitess prints `DEFAULT CURRENT_TIMESTAMP` as `current_timestamp()`,
//     while the catalog stores it without the empty parens. Same for
//     other magic time-functions and `now`.
//   - vitess lower-cases keywords; the catalog upper-cases the magic
//     functions. We upper-case to align.
func normalizeDefaultExpr(s string) string {
	t := strings.TrimSpace(s)
	for _, fn := range []string{
		"current_timestamp", "current_date", "current_time", "now",
		"localtime", "localtimestamp", "utc_date", "utc_time",
		"utc_timestamp",
	} {
		if strings.EqualFold(t, fn+"()") {
			return strings.ToUpper(fn)
		}
		// Preserve precision in `current_timestamp(6)` etc.
		lower := strings.ToLower(t)
		if strings.HasPrefix(lower, fn+"(") && strings.HasSuffix(lower, ")") {
			arg := lower[len(fn)+1 : len(lower)-1]
			return strings.ToUpper(fn) + "(" + arg + ")"
		}
	}
	return t
}

// applyAlterTable supports two forms in desired-side SQL:
//   - `ALTER TABLE t ADD CONSTRAINT … FOREIGN KEY (…)` (and CHECK)
//   - `CREATE INDEX … ON t (…)` (vitess parses this as AlterTable with an
//     AddIndexDefinition option)
//
// Other ALTER subcommands are out of scope for v1: callers express the
// desired state via CREATE TABLE only.
func applyAlterTable(tables *orderedmap.Map[string, *model.Table], s *sqlparser.AlterTable, defaultDB string) error {
	db, err := resolveStmtDB(s.Table.Qualifier.String(), defaultDB, "ALTER TABLE", s.Table.Name.String())
	if err != nil {
		return err
	}
	fqtn := model.Ident(db, s.Table.Name.String())
	t, ok := tables.GetOk(fqtn)
	if !ok {
		return fmt.Errorf("ALTER TABLE on unknown table %s", fqtn)
	}
	for _, opt := range s.AlterOptions {
		switch o := opt.(type) {
		case *sqlparser.AddConstraintDefinition:
			if err := addTableConstraint(t, o.ConstraintDefinition, defaultDB); err != nil {
				return err
			}
		case *sqlparser.AddIndexDefinition:
			if err := addIndex(t, o.IndexDefinition); err != nil {
				return err
			}
		}
	}
	return nil
}

func autoFKName(table, col string) string {
	return table + "_ibfk_" + col
}

// applyInlineColumnKey promotes a column-level KeyOpt (vitess's
// representation of inline `PRIMARY KEY` / `UNIQUE [KEY]` written
// against a single column) to the same `Constraints` / `Indexes`
// shape the table-level path produces.
//
//   - `ColKeyNone`: the common case (no inline key) — fall through.
//   - `ColKeyPrimary`, `ColKey` (bare `KEY`): both promote to a
//     PRIMARY constraint. MySQL accepts `col INT KEY` as a synonym
//     for `col INT PRIMARY KEY` (creates a real PRIMARY KEY,
//     forces NOT NULL on the column, errors with "Multiple primary
//     key defined" if two columns each carry it). Without this
//     promotion, the user's PK declaration silently disappears
//     from myschema's model — emitted DDL would have no PRIMARY
//     KEY and no NOT NULL on the column.
//   - `ColKeyUnique` / `ColKeyUniqueKey`: promote to a UNIQUE
//     index named after the column.
//   - `ColKeySpatialKey` / `ColKeyFulltextKey`: unreachable from
//     valid SQL — vitess's own grammar rejects the column-level
//     shapes (`col GEOMETRY SPATIAL KEY`, `col TEXT FULLTEXT
//     KEY`) with a syntax error. MySQL refuses the same shapes.
//     The enum values exist in vitess but no real input produces
//     them, so we leave the arms unhandled rather than adding
//     errors for an unreachable path.
func applyInlineColumnKey(t *model.Table, colName string, k sqlparser.ColumnKeyOption) error {
	switch k {
	case sqlparser.ColKeyPrimary, sqlparser.ColKey:
		// Skip if a table-level PRIMARY KEY was already promoted —
		// not strictly necessary since addIndex's IndexTypePrimary
		// case unconditionally overwrites Constraints["PRIMARY"] /
		// Indexes["PRIMARY"], so a duplicate here would land on the
		// same end state when the column lists match. The skip just
		// keeps this branch a no-op for the inline-form-after-
		// table-level shape (which a parser pass order change could
		// otherwise expose). Truly conflicting PK column lists are
		// user error — MySQL rejects them at apply time.
		if _, ok := t.Constraints.GetOk("PRIMARY"); ok {
			return nil
		}
		parts := []model.IndexPart{{Column: colName}}
		def, _ := indexBodySQLFromParts(parts)
		t.Constraints.Set("PRIMARY", &model.Constraint{
			Name:       "PRIMARY",
			Type:       model.PrimaryKeyConstraint,
			Definition: def,
			Columns:    []string{colName},
		})
		t.Indexes.Set("PRIMARY", &model.Index{
			Name: "PRIMARY", Database: t.Database, Table: t.Name,
			Primary: true, Parts: parts,
		})
		// MySQL silently forces NOT NULL on PK columns. Mirror that
		// so the dump round-trip stays stable — same as the
		// table-level path at parser.go's IndexTypePrimary case.
		if col, ok := t.Columns.GetOk(colName); ok {
			col.NotNull = true
		}
	case sqlparser.ColKeyUnique, sqlparser.ColKeyUniqueKey:
		// Duplicate-index guard. Defensive — unreachable in the
		// current architecture: the column loop runs before the
		// table-level Indexes loop, and duplicate column names are
		// already rejected upstream (see "duplicate column" above),
		// so `t.Indexes[colName]` cannot be set when this case
		// fires. Kept for symmetry with addIndex's IndexTypeUnique
		// branch and to fail closed if a future refactor changes the
		// pass order.
		if _, dup := t.Indexes.GetOk(colName); dup {
			return fmt.Errorf("duplicate index: %s on %s", colName, t.FQTN())
		}
		t.Indexes.Set(colName, &model.Index{
			Name: colName, Database: t.Database, Table: t.Name,
			KeyType: model.IndexUnique,
			Parts:   []model.IndexPart{{Column: colName}},
		})
	}
	return nil
}

func autoCheckName(table string, n int) string {
	return fmt.Sprintf("%s_chk_%d", table, n+1)
}
