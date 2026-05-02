package catalog

import (
	"context"
	"errors"
	"fmt"
	"strings"

	mysqldrv "github.com/go-sql-driver/mysql"
	"vitess.io/vitess/go/vt/sqlparser"

	"github.com/winebarrel/myschema/model"
	"github.com/winebarrel/orderedmap"
)

// MySQL surfaces "the information_schema view doesn't exist on this server
// version" via two distinct error codes — historically as ER_UNKNOWN_TABLE
// (1109, "Unknown table 'X' in information_schema") and on newer servers
// as ER_NO_SUCH_TABLE (1146). We treat both as "this MySQL is too old to
// have CHECK_CONSTRAINTS" rather than a hard error.
const (
	mysqlErrUnknownTable = 1109
	mysqlErrNoSuchTable  = 1146
)

// Tables loads every table in the configured databases plus its columns,
// constraints, foreign keys, and indexes.
func (c *Catalog) Tables(ctx context.Context) (*orderedmap.Map[string, *model.Table], error) {
	if err := c.ping(ctx); err != nil {
		return nil, err
	}

	out := orderedmap.New[string, *model.Table]()
	q := `
SELECT TABLE_SCHEMA, TABLE_NAME, ENGINE, TABLE_COLLATION, TABLE_COMMENT
FROM   information_schema.TABLES
WHERE  TABLE_SCHEMA = ?
  AND  TABLE_TYPE = 'BASE TABLE'
ORDER  BY TABLE_NAME`

	rows, err := c.conn.QueryContext(ctx, q, c.database)
	if err != nil {
		return nil, fmt.Errorf("catalog: list tables: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	for rows.Next() {
		var (
			db, name string
			engine   *string
			coll     *string
			comment  string
		)
		if err := rows.Scan(&db, &name, &engine, &coll, &comment); err != nil {
			return nil, fmt.Errorf("catalog: scan tables: %w", err)
		}
		t := &model.Table{
			Database:    db,
			Name:        name,
			Engine:      engine,
			Collation:   coll,
			Columns:     orderedmap.New[string, *model.Column](),
			Constraints: orderedmap.New[string, *model.Constraint](),
			ForeignKeys: orderedmap.New[string, *model.ForeignKey](),
			Indexes:     orderedmap.New[string, *model.Index](),
		}
		if comment != "" {
			cm := comment
			t.Comment = &cm
		}
		out.Set(model.Ident(db, name), t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("catalog: iterate tables: %w", err)
	}

	for _, t := range out.CollectValues() {
		if err := c.loadColumns(ctx, t); err != nil {
			return nil, err
		}
		if err := c.loadIndexes(ctx, t); err != nil {
			return nil, err
		}
		if err := c.loadCheckConstraints(ctx, t); err != nil {
			return nil, err
		}
		if err := c.loadForeignKeys(ctx, t); err != nil {
			return nil, err
		}
	}

	return out, nil
}

func (c *Catalog) loadColumns(ctx context.Context, t *model.Table) error {
	q := `
SELECT COLUMN_NAME, COLUMN_TYPE, IS_NULLABLE, COLUMN_DEFAULT, EXTRA,
       COLLATION_NAME, CHARACTER_SET_NAME, COLUMN_COMMENT, GENERATION_EXPRESSION
FROM   information_schema.COLUMNS
WHERE  TABLE_SCHEMA = ? AND TABLE_NAME = ?
ORDER  BY ORDINAL_POSITION`
	rows, err := c.conn.QueryContext(ctx, q, t.Database, t.Name)
	if err != nil {
		return fmt.Errorf("catalog: list columns for %s: %w", t.FQTN(), err)
	}
	defer rows.Close() //nolint:errcheck

	for rows.Next() {
		var (
			name, colType, isNullable, extra string
			defVal, coll, charset            *string
			comment, genExpr                 string
		)
		if err := rows.Scan(&name, &colType, &isNullable, &defVal, &extra, &coll, &charset, &comment, &genExpr); err != nil {
			return fmt.Errorf("catalog: scan columns for %s: %w", t.FQTN(), err)
		}
		col := &model.Column{
			Name:         name,
			TypeName:     strings.ToLower(colType),
			NotNull:      strings.EqualFold(isNullable, "NO"),
			Collation:    coll,
			CharacterSet: charset,
		}
		if defVal != nil {
			d := normalizeColumnDefault(strings.ToLower(colType), *defVal)
			col.Default = &d
		}
		extraUp := strings.ToUpper(extra)
		if strings.Contains(extraUp, "AUTO_INCREMENT") {
			col.AutoIncrement = true
		}
		if strings.Contains(extraUp, "ON UPDATE ") {
			ou := strings.TrimPrefix(extraUp, "DEFAULT_GENERATED ON UPDATE ")
			ou = strings.TrimPrefix(ou, "ON UPDATE ")
			col.OnUpdate = &ou
		}
		if genExpr != "" {
			ge := genExpr
			col.Generated = &ge
			col.Stored = strings.Contains(extraUp, "STORED GENERATED")
		}
		if comment != "" {
			cm := comment
			col.Comment = &cm
		}
		t.Columns.Set(name, col)
	}
	return rows.Err()
}

func (c *Catalog) loadIndexes(ctx context.Context, t *model.Table) error {
	// information_schema.STATISTICS lists one row per (index, sequence) so we
	// have to gather columns by seq_in_index. A second pass turns the per-index
	// metadata into Indexes; PRIMARY also gets mirrored as a Constraint.
	q := `
SELECT INDEX_NAME, NON_UNIQUE, SEQ_IN_INDEX, COLUMN_NAME, SUB_PART, INDEX_TYPE,
       COLLATION, EXPRESSION, INDEX_COMMENT, IS_VISIBLE
FROM   information_schema.STATISTICS
WHERE  TABLE_SCHEMA = ? AND TABLE_NAME = ?
ORDER  BY INDEX_NAME, SEQ_IN_INDEX`
	rows, err := c.conn.QueryContext(ctx, q, t.Database, t.Name)
	if err != nil {
		return fmt.Errorf("catalog: list indexes for %s: %w", t.FQTN(), err)
	}
	defer rows.Close() //nolint:errcheck

	type indexRow struct {
		nonUnique int
		indexType string
		comment   string
		visible   string
		parts     []model.IndexPart
	}
	idxs := map[string]*indexRow{}
	order := []string{}
	for rows.Next() {
		var (
			name       string
			nonUnique  int
			seq        int
			colName    *string
			subPart    *int
			indexType  string
			collation  *string
			expression *string
			comment    string
			visible    string
		)
		if err := rows.Scan(&name, &nonUnique, &seq, &colName, &subPart, &indexType, &collation, &expression, &comment, &visible); err != nil {
			return fmt.Errorf("catalog: scan indexes for %s: %w", t.FQTN(), err)
		}
		ir, ok := idxs[name]
		if !ok {
			ir = &indexRow{nonUnique: nonUnique, indexType: indexType, comment: comment, visible: visible}
			idxs[name] = ir
			order = append(order, name)
		}
		part := model.IndexPart{}
		if expression != nil {
			part.Expr = *expression
		} else if colName != nil {
			part.Column = *colName
		}
		if subPart != nil {
			part.Length = *subPart
		}
		if collation != nil && *collation == "D" {
			part.Desc = true
		}
		ir.parts = append(ir.parts, part)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, name := range order {
		ir := idxs[name]
		idx := &model.Index{
			Name:      name,
			Database:  t.Database,
			Table:     t.Name,
			Parts:     ir.parts,
			IndexType: ir.indexType,
			Invisible: strings.EqualFold(ir.visible, "NO"),
			Primary:   name == "PRIMARY",
		}
		if ir.comment != "" {
			cm := ir.comment
			idx.Comment = &cm
		}
		if name != "PRIMARY" {
			if ir.nonUnique == 0 {
				idx.KeyType = model.IndexUnique
			}
			// MySQL 8.x distinguishes FULLTEXT/SPATIAL via INDEX_TYPE.
			switch strings.ToUpper(idx.IndexType) {
			case "FULLTEXT":
				idx.KeyType = model.IndexFulltext
				idx.IndexType = ""
			case "SPATIAL":
				idx.KeyType = model.IndexSpatial
				idx.IndexType = ""
			}
		}
		t.Indexes.Set(name, idx)

		if name == "PRIMARY" {
			cols := make([]string, 0, len(idx.Parts))
			defParts := make([]string, 0, len(idx.Parts))
			for _, p := range idx.Parts {
				if p.Column != "" {
					cols = append(cols, p.Column)
				}
				defParts = append(defParts, p.SQL())
			}
			t.Constraints.Set("PRIMARY", &model.Constraint{
				Name:       "PRIMARY",
				Type:       model.PrimaryKeyConstraint,
				Definition: "(" + strings.Join(defParts, ", ") + ")",
				Columns:    cols,
			})
		}
	}
	return nil
}

func (c *Catalog) loadCheckConstraints(ctx context.Context, t *model.Table) error {
	// information_schema.CHECK_CONSTRAINTS exists in MySQL 8.0.16+; missing on
	// older servers. Treat a missing table as "no checks" so we keep working.
	// CHECK_CONSTRAINTS has no TABLE_NAME column, so JOIN TABLE_CONSTRAINTS
	// (CONSTRAINT_TYPE = 'CHECK') to scope by table.
	q := `
SELECT cc.CONSTRAINT_NAME, cc.CHECK_CLAUSE
FROM   information_schema.CHECK_CONSTRAINTS cc
JOIN   information_schema.TABLE_CONSTRAINTS tc
       ON tc.CONSTRAINT_SCHEMA = cc.CONSTRAINT_SCHEMA
      AND tc.CONSTRAINT_NAME   = cc.CONSTRAINT_NAME
WHERE  tc.TABLE_SCHEMA    = ?
  AND  tc.TABLE_NAME      = ?
  AND  tc.CONSTRAINT_TYPE = 'CHECK'`
	rows, err := c.conn.QueryContext(ctx, q, t.Database, t.Name)
	if err != nil {
		var mErr *mysqldrv.MySQLError
		if errors.As(err, &mErr) {
			switch mErr.Number {
			case mysqlErrNoSuchTable, mysqlErrUnknownTable:
				return nil
			}
		}
		return fmt.Errorf("catalog: list check constraints for %s: %w", t.FQTN(), err)
	}
	defer rows.Close() //nolint:errcheck
	for rows.Next() {
		var name, clause string
		if err := rows.Scan(&name, &clause); err != nil {
			return fmt.Errorf("catalog: scan check constraints for %s: %w", t.FQTN(), err)
		}
		t.Constraints.Set(name, &model.Constraint{
			Name:       name,
			Type:       model.CheckConstraint,
			Definition: "CHECK (" + clause + ")",
			Enforced:   true,
		})
	}
	return rows.Err()
}

func (c *Catalog) loadForeignKeys(ctx context.Context, t *model.Table) error {
	// REFERENTIAL_CONSTRAINTS gives ON DELETE / ON UPDATE / MATCH; KEY_COLUMN_USAGE
	// gives the column ↔ referenced-column mapping. Joined here for one round trip.
	q := `
SELECT k.CONSTRAINT_NAME, k.COLUMN_NAME, k.REFERENCED_TABLE_SCHEMA, k.REFERENCED_TABLE_NAME, k.REFERENCED_COLUMN_NAME,
       r.UPDATE_RULE, r.DELETE_RULE, r.MATCH_OPTION
FROM   information_schema.KEY_COLUMN_USAGE k
JOIN   information_schema.REFERENTIAL_CONSTRAINTS r
       ON r.CONSTRAINT_SCHEMA = k.TABLE_SCHEMA
      AND r.CONSTRAINT_NAME   = k.CONSTRAINT_NAME
      AND r.TABLE_NAME        = k.TABLE_NAME
WHERE  k.TABLE_SCHEMA = ? AND k.TABLE_NAME = ?
ORDER  BY k.CONSTRAINT_NAME, k.ORDINAL_POSITION`
	rows, err := c.conn.QueryContext(ctx, q, t.Database, t.Name)
	if err != nil {
		return fmt.Errorf("catalog: list foreign keys for %s: %w", t.FQTN(), err)
	}
	defer rows.Close() //nolint:errcheck

	type fkAcc struct {
		fk *model.ForeignKey
	}
	fks := map[string]*fkAcc{}
	order := []string{}
	for rows.Next() {
		var (
			name, col               string
			refDB, refTable         string
			refCol                  string
			updRule, delRule, match string
		)
		if err := rows.Scan(&name, &col, &refDB, &refTable, &refCol, &updRule, &delRule, &match); err != nil {
			return fmt.Errorf("catalog: scan foreign keys for %s: %w", t.FQTN(), err)
		}
		acc, ok := fks[name]
		if !ok {
			acc = &fkAcc{fk: &model.ForeignKey{
				Name:      name,
				Database:  t.Database,
				Table:     t.Name,
				RefDB:     refDB,
				RefTable:  refTable,
				OnUpdate:  normalizeRefOpt(updRule),
				OnDelete:  normalizeRefOpt(delRule),
				MatchType: normalizeMatch(match),
			}}
			fks[name] = acc
			order = append(order, name)
		}
		acc.fk.Columns = append(acc.fk.Columns, col)
		acc.fk.RefCols = append(acc.fk.RefCols, refCol)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, n := range order {
		t.ForeignKeys.Set(n, fks[n].fk)
	}
	return nil
}

// normalizeColumnDefault wraps the catalog's bareword default in single
// quotes when MySQL stored an unquoted string literal — e.g. `G` for an
// enum, `hello` for a varchar. Vitess parses such barewords as a
// *sqlparser.ColName (column reference) rather than a string literal, and
// later round-trips break.
//
// We delegate the classification to vitess: try to parse `SELECT <def>`,
// and if the resulting expression is a *ColName, MySQL stored it as a
// quoteless literal — wrap it. Anything else (Literal, NullVal, BoolVal,
// CurTimeFuncExpr, FuncExpr, BinaryExpr, …) is already valid SQL.
//
// typeName is unused now but kept on the signature to mark this as
// type-aware logic (we may need to special-case BIT or BLOB defaults
// later — see TODO.md).
func normalizeColumnDefault(typeName, def string) string {
	_ = typeName
	if def == "" {
		return def
	}
	p, err := sqlparser.New(sqlparser.Options{})
	if err != nil {
		return def
	}
	stmt, err := p.Parse("SELECT " + def)
	if err != nil {
		return def
	}
	sel, ok := stmt.(*sqlparser.Select)
	if !ok || sel.SelectExprs == nil || len(sel.SelectExprs.Exprs) != 1 {
		return def
	}
	ae, ok := sel.SelectExprs.Exprs[0].(*sqlparser.AliasedExpr)
	if !ok {
		return def
	}
	if _, isCol := ae.Expr.(*sqlparser.ColName); !isCol {
		return def
	}
	return "'" + strings.ReplaceAll(def, "'", "''") + "'"
}

func normalizeRefOpt(s string) string {
	switch strings.ToUpper(s) {
	case "RESTRICT", "NO ACTION":
		return ""
	case "CASCADE", "SET NULL", "SET DEFAULT":
		return strings.ToUpper(s)
	}
	return ""
}

func normalizeMatch(s string) string {
	if s == "" || strings.EqualFold(s, "NONE") {
		return ""
	}
	return strings.ToUpper(s)
}
