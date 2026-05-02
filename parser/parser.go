// Package parser turns desired-schema SQL into the model.* representation.
// Built on github.com/pingcap/tidb/pkg/parser (the maintained successor of
// github.com/pingcap/parser; see AGENTS.md for the rationale).
package parser

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/pingcap/tidb/pkg/parser"
	"github.com/pingcap/tidb/pkg/parser/ast"
	"github.com/pingcap/tidb/pkg/parser/format"
	_ "github.com/pingcap/tidb/pkg/parser/test_driver" // registers the expression value driver
	"github.com/pingcap/tidb/pkg/parser/types"
	"github.com/winebarrel/myschema/model"
	"github.com/winebarrel/orderedmap"
)

func init() {
	// Strip the default display widths that MySQL 8.x removed (e.g. "bigint(20)"
	// → "bigint"). Without this, the parser side never matches the catalog side
	// on an 8.0 server.
	types.TiDBStrictIntegerDisplayWidth = true
}

// ParseResult holds everything the parser produces.
type ParseResult struct {
	Tables *orderedmap.Map[string, *model.Table]
	Views  *orderedmap.Map[string, *model.View]
}

// ReadSQLFile reads a SQL file (or "-" for stdin).
func ReadSQLFile(path string) (string, error) {
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
		s, err := ReadSQLFile(p)
		if err != nil {
			return nil, err
		}
		sqls = append(sqls, s)
	}
	return ParseSQL(strings.Join(sqls, "\n"), defaultDB)
}

// ParseSQL parses a SQL string into a ParseResult.
func ParseSQL(sql, defaultDB string) (*ParseResult, error) {
	p := parser.New()
	stmts, _, err := p.Parse(sql, "", "")
	if err != nil {
		return nil, fmt.Errorf("failed to parse SQL: %w", err)
	}

	tables := orderedmap.New[string, *model.Table]()
	views := orderedmap.New[string, *model.View]()

	for _, stmt := range stmts {
		switch s := stmt.(type) {
		case *ast.CreateTableStmt:
			t, err := parseCreateTable(s, defaultDB)
			if err != nil {
				return nil, err
			}
			if _, dup := tables.GetOk(t.FQTN()); dup {
				return nil, fmt.Errorf("duplicate table: %s", t.FQTN())
			}
			tables.Set(t.FQTN(), t)

		case *ast.CreateIndexStmt:
			if err := applyCreateIndex(tables, s, defaultDB); err != nil {
				return nil, err
			}

		case *ast.AlterTableStmt:
			if err := applyAlterTable(tables, s, defaultDB); err != nil {
				return nil, err
			}

		case *ast.CreateViewStmt:
			v, err := parseCreateView(s, defaultDB)
			if err != nil {
				return nil, err
			}
			if _, dup := views.GetOk(v.FQVN()); dup {
				return nil, fmt.Errorf("duplicate view: %s", v.FQVN())
			}
			views.Set(v.FQVN(), v)

		default:
			// Skip statements we don't model (CREATE DATABASE, COMMENT,
			// SET, GRANT, etc). Unknown DDL is silently ignored; future work
			// could surface a warning.
		}
	}

	return &ParseResult{Tables: tables, Views: views}, nil
}

func dbName(schema, defaultDB string) string {
	if schema != "" {
		return schema
	}
	return defaultDB
}

func parseCreateTable(s *ast.CreateTableStmt, defaultDB string) (*model.Table, error) {
	t := &model.Table{
		Database:    dbName(s.Table.Schema.O, defaultDB),
		Name:        s.Table.Name.O,
		Columns:     orderedmap.New[string, *model.Column](),
		Constraints: orderedmap.New[string, *model.Constraint](),
		ForeignKeys: orderedmap.New[string, *model.ForeignKey](),
		Indexes:     orderedmap.New[string, *model.Index](),
	}

	for _, col := range s.Cols {
		c, err := parseColumnDef(col)
		if err != nil {
			return nil, fmt.Errorf("table %s: column %s: %w", t.FQTN(), col.Name.Name.O, err)
		}
		if _, dup := t.Columns.GetOk(c.Name); dup {
			return nil, fmt.Errorf("duplicate column %s.%s", t.FQTN(), c.Name)
		}
		t.Columns.Set(c.Name, c)

		// Inline column-level PRIMARY KEY / UNIQUE / REFERENCES become table-level
		// constraints so the diff sees them the same as catalog rows.
		for _, opt := range col.Options {
			switch opt.Tp {
			case ast.ColumnOptionPrimaryKey:
				con := &model.Constraint{
					Name:       "PRIMARY",
					Type:       model.PrimaryKeyConstraint,
					Definition: "(" + model.Ident(c.Name) + ")",
					Columns:    []string{c.Name},
				}
				if _, dup := t.Constraints.GetOk("PRIMARY"); !dup {
					t.Constraints.Set("PRIMARY", con)
				}
				// Primary-key columns are implicitly NOT NULL.
				c.NotNull = true
				// Mirror as Index so diff treats it consistently.
				t.Indexes.Set("PRIMARY", &model.Index{
					Name:     "PRIMARY",
					Database: t.Database, Table: t.Name, Primary: true,
					Parts: []model.IndexPart{{Column: c.Name}},
				})
			case ast.ColumnOptionUniqKey:
				name := c.Name
				idx := &model.Index{
					Name:     name,
					Database: t.Database, Table: t.Name,
					KeyType: model.IndexUnique,
					Parts:   []model.IndexPart{{Column: c.Name}},
				}
				if _, dup := t.Indexes.GetOk(name); !dup {
					t.Indexes.Set(name, idx)
				}
			case ast.ColumnOptionReference:
				if opt.Refer == nil {
					continue
				}
				fk, err := buildFK("", t, []string{c.Name}, opt.Refer, defaultDB)
				if err != nil {
					return nil, err
				}
				if fk.Name == "" {
					fk.Name = autoFKName(t.Name, c.Name)
				}
				if _, dup := t.ForeignKeys.GetOk(fk.Name); !dup {
					t.ForeignKeys.Set(fk.Name, fk)
				}
			}
		}
	}

	for _, con := range s.Constraints {
		if err := addConstraint(t, con, defaultDB); err != nil {
			return nil, err
		}
	}

	for _, opt := range s.Options {
		applyTableOption(t, opt)
	}

	return t, nil
}

func parseColumnDef(col *ast.ColumnDef) (*model.Column, error) {
	c := &model.Column{
		Name:     col.Name.Name.O,
		TypeName: normalizeTypeName(col.Tp.InfoSchemaStr()),
	}

	for _, opt := range col.Options {
		switch opt.Tp {
		case ast.ColumnOptionNotNull:
			c.NotNull = true
		case ast.ColumnOptionNull:
			c.NotNull = false
		case ast.ColumnOptionDefaultValue:
			if opt.Expr != nil {
				v, err := restoreExpr(opt.Expr)
				if err != nil {
					return nil, fmt.Errorf("default: %w", err)
				}
				c.Default = &v
			}
		case ast.ColumnOptionAutoIncrement:
			c.AutoIncrement = true
		case ast.ColumnOptionOnUpdate:
			if opt.Expr != nil {
				v, err := restoreExpr(opt.Expr)
				if err != nil {
					return nil, fmt.Errorf("on update: %w", err)
				}
				c.OnUpdate = &v
			}
		case ast.ColumnOptionComment:
			if opt.Expr != nil {
				v, err := restoreExpr(opt.Expr)
				if err != nil {
					return nil, fmt.Errorf("comment: %w", err)
				}
				v = strings.TrimSuffix(strings.TrimPrefix(v, "'"), "'")
				c.Comment = &v
			}
		case ast.ColumnOptionCollate:
			coll := opt.StrValue
			c.Collation = &coll
		case ast.ColumnOptionGenerated:
			if opt.Expr != nil {
				v, err := restoreExpr(opt.Expr)
				if err != nil {
					return nil, fmt.Errorf("generated: %w", err)
				}
				c.Generated = &v
				c.Stored = opt.Stored
			}
		}
	}

	return c, nil
}

func addConstraint(t *model.Table, con *ast.Constraint, defaultDB string) error {
	switch con.Tp {
	case ast.ConstraintPrimaryKey:
		def, cols, err := indexBodySQL(con.Keys)
		if err != nil {
			return err
		}
		c := &model.Constraint{
			Name:       "PRIMARY",
			Type:       model.PrimaryKeyConstraint,
			Definition: def,
			Columns:    cols,
		}
		t.Constraints.Set("PRIMARY", c)
		// PK columns are implicitly NOT NULL.
		for _, name := range cols {
			if col, ok := t.Columns.GetOk(name); ok {
				col.NotNull = true
			}
		}
		// Mirror as Index for diff symmetry.
		parts, err := indexParts(con.Keys)
		if err != nil {
			return err
		}
		t.Indexes.Set("PRIMARY", &model.Index{
			Name: "PRIMARY", Database: t.Database, Table: t.Name,
			Primary: true, Parts: parts,
		})

	case ast.ConstraintUniq, ast.ConstraintUniqKey, ast.ConstraintUniqIndex:
		// Treat every non-PK UNIQUE clause as a UNIQUE INDEX (MySQL stores them
		// in information_schema.statistics — which our catalog reader uses too —
		// so this lines up cleanly with the catalog side).
		parts, err := indexParts(con.Keys)
		if err != nil {
			return err
		}
		name := con.Name
		if name == "" && len(parts) > 0 {
			name = parts[0].Column
		}
		idx := &model.Index{
			Name:     name,
			Database: t.Database, Table: t.Name,
			KeyType: model.IndexUnique,
			Parts:   parts,
		}
		if con.Option != nil {
			applyIndexOption(idx, con.Option)
		}
		if _, dup := t.Indexes.GetOk(name); dup {
			return fmt.Errorf("duplicate index: %s on %s", name, t.FQTN())
		}
		t.Indexes.Set(name, idx)

	case ast.ConstraintKey, ast.ConstraintIndex:
		parts, err := indexParts(con.Keys)
		if err != nil {
			return err
		}
		name := con.Name
		if name == "" && len(parts) > 0 {
			name = parts[0].Column
		}
		idx := &model.Index{
			Name:     name,
			Database: t.Database, Table: t.Name,
			Parts: parts,
		}
		if con.Option != nil {
			applyIndexOption(idx, con.Option)
		}
		if _, dup := t.Indexes.GetOk(name); dup {
			return fmt.Errorf("duplicate index: %s on %s", name, t.FQTN())
		}
		t.Indexes.Set(name, idx)

	case ast.ConstraintForeignKey:
		cols, err := indexColumns(con.Keys)
		if err != nil {
			return err
		}
		fk, err := buildFK(con.Name, t, cols, con.Refer, defaultDB)
		if err != nil {
			return err
		}
		if fk.Name == "" {
			fk.Name = autoFKName(t.Name, cols[0])
		}
		if _, dup := t.ForeignKeys.GetOk(fk.Name); dup {
			return fmt.Errorf("duplicate foreign key: %s on %s", fk.Name, t.FQTN())
		}
		t.ForeignKeys.Set(fk.Name, fk)

	case ast.ConstraintCheck:
		if con.Expr == nil {
			return nil
		}
		expr, err := restoreExpr(con.Expr)
		if err != nil {
			return err
		}
		name := con.Name
		if name == "" {
			name = autoCheckName(t.Name, t.Constraints.Len())
		}
		c := &model.Constraint{
			Name:       name,
			Type:       model.CheckConstraint,
			Definition: "CHECK (" + expr + ")",
			Enforced:   con.Enforced,
		}
		if _, dup := t.Constraints.GetOk(name); dup {
			return fmt.Errorf("duplicate check constraint: %s on %s", name, t.FQTN())
		}
		t.Constraints.Set(name, c)
	}

	return nil
}

func applyTableOption(t *model.Table, opt *ast.TableOption) {
	switch opt.Tp {
	case ast.TableOptionEngine:
		v := opt.StrValue
		t.Engine = &v
	case ast.TableOptionCharset:
		v := opt.StrValue
		t.Charset = &v
	case ast.TableOptionCollate:
		v := opt.StrValue
		t.Collation = &v
	case ast.TableOptionComment:
		v := opt.StrValue
		t.Comment = &v
	case ast.TableOptionAutoIncrement:
		v := opt.UintValue
		t.AutoIncrement = &v
	}
}

func applyCreateIndex(tables *orderedmap.Map[string, *model.Table], s *ast.CreateIndexStmt, defaultDB string) error {
	fqtn := model.Ident(dbName(s.Table.Schema.O, defaultDB), s.Table.Name.O)
	t, ok := tables.GetOk(fqtn)
	if !ok {
		return fmt.Errorf("CREATE INDEX %s on unknown table %s", s.IndexName, fqtn)
	}

	parts, err := indexParts(s.IndexPartSpecifications)
	if err != nil {
		return err
	}
	idx := &model.Index{
		Name:     s.IndexName,
		Database: t.Database, Table: t.Name,
		Parts: parts,
	}
	switch s.KeyType {
	case ast.IndexKeyTypeUnique:
		idx.KeyType = model.IndexUnique
	case ast.IndexKeyTypeFulltext:
		idx.KeyType = model.IndexFulltext
	case ast.IndexKeyTypeSpatial:
		idx.KeyType = model.IndexSpatial
	}
	if s.IndexOption != nil {
		applyIndexOption(idx, s.IndexOption)
	}
	if _, dup := t.Indexes.GetOk(idx.Name); dup {
		return fmt.Errorf("duplicate index: %s on %s", idx.Name, fqtn)
	}
	t.Indexes.Set(idx.Name, idx)
	return nil
}

func applyAlterTable(tables *orderedmap.Map[string, *model.Table], s *ast.AlterTableStmt, defaultDB string) error {
	fqtn := model.Ident(dbName(s.Table.Schema.O, defaultDB), s.Table.Name.O)
	t, ok := tables.GetOk(fqtn)
	if !ok {
		return fmt.Errorf("ALTER TABLE on unknown table %s", fqtn)
	}
	for _, spec := range s.Specs {
		if spec.Tp == ast.AlterTableAddConstraint && spec.Constraint != nil {
			if err := addConstraint(t, spec.Constraint, defaultDB); err != nil {
				return err
			}
		}
		// Other AlterTableSpec types (ADD COLUMN, MODIFY COLUMN, DROP COLUMN, …)
		// in the desired-side SQL are out of scope for v1: callers are expected
		// to express the desired state via CREATE TABLE only, and the diff
		// engine generates ALTERs against the current state.
	}
	return nil
}

func applyIndexOption(idx *model.Index, opt *ast.IndexOption) {
	if opt.Tp.String() != "" { // BTREE / HASH / RTREE
		idx.IndexType = strings.ToUpper(opt.Tp.String())
	}
	if opt.Comment != "" {
		c := opt.Comment
		idx.Comment = &c
	}
	if opt.Visibility == ast.IndexVisibilityInvisible {
		idx.Invisible = true
	}
}

func indexParts(keys []*ast.IndexPartSpecification) ([]model.IndexPart, error) {
	out := make([]model.IndexPart, 0, len(keys))
	for _, k := range keys {
		// pingcap parser uses -1 (UnspecifiedLength) when no prefix length is
		// given. The catalog reader returns 0 for the same case (NULL SUB_PART),
		// so normalise to 0 here.
		length := k.Length
		if length < 0 {
			length = 0
		}
		p := model.IndexPart{Length: length, Desc: k.Desc}
		if k.Expr != nil {
			expr, err := restoreExpr(k.Expr)
			if err != nil {
				return nil, err
			}
			p.Expr = expr
		} else if k.Column != nil {
			p.Column = k.Column.Name.O
		}
		out = append(out, p)
	}
	return out, nil
}

func indexColumns(keys []*ast.IndexPartSpecification) ([]string, error) {
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		if k.Column == nil {
			return nil, fmt.Errorf("expected column in foreign key key list")
		}
		out = append(out, k.Column.Name.O)
	}
	return out, nil
}

func indexBodySQL(keys []*ast.IndexPartSpecification) (def string, cols []string, err error) {
	parts, err := indexParts(keys)
	if err != nil {
		return "", nil, err
	}
	var b strings.Builder
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
	return b.String(), cols, nil
}

func buildFK(name string, t *model.Table, cols []string, ref *ast.ReferenceDef, defaultDB string) (*model.ForeignKey, error) {
	if ref == nil || ref.Table == nil {
		return nil, fmt.Errorf("foreign key %s on %s: missing REFERENCES target", name, t.FQTN())
	}
	refCols, err := indexColumns(ref.IndexPartSpecifications)
	if err != nil {
		return nil, err
	}
	fk := &model.ForeignKey{
		Name:     name,
		Database: t.Database, Table: t.Name,
		Columns:  cols,
		RefDB:    dbName(ref.Table.Schema.O, defaultDB),
		RefTable: ref.Table.Name.O,
		RefCols:  refCols,
	}
	if ref.OnDelete != nil {
		fk.OnDelete = referenceOptionString(ref.OnDelete.ReferOpt)
	}
	if ref.OnUpdate != nil {
		fk.OnUpdate = referenceOptionString(ref.OnUpdate.ReferOpt)
	}
	switch ref.Match {
	case ast.MatchFull:
		fk.MatchType = "FULL"
	case ast.MatchPartial:
		fk.MatchType = "PARTIAL"
	case ast.MatchSimple:
		fk.MatchType = "SIMPLE"
	}
	return fk, nil
}

func referenceOptionString(opt ast.ReferOptionType) string {
	switch opt {
	case ast.ReferOptionRestrict:
		return "RESTRICT"
	case ast.ReferOptionCascade:
		return "CASCADE"
	case ast.ReferOptionSetNull:
		return "SET NULL"
	case ast.ReferOptionNoAction:
		return "NO ACTION"
	case ast.ReferOptionSetDefault:
		return "SET DEFAULT"
	}
	return ""
}

func autoFKName(table, col string) string {
	return table + "_ibfk_" + col
}

func autoCheckName(table string, n int) string {
	return fmt.Sprintf("%s_chk_%d", table, n+1)
}

func restoreExpr(node ast.Node) (string, error) {
	var buf bytes.Buffer
	ctx := format.NewRestoreCtx(format.DefaultRestoreFlags, &buf)
	if err := node.Restore(ctx); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// normalizeTypeName lower-cases and strips redundant spacing so parser-side
// types match information_schema's COLUMN_TYPE values.
func normalizeTypeName(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}
