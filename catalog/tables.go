package catalog

import (
	"context"
	"errors"
	"fmt"
	"strings"

	mysqldrv "github.com/go-sql-driver/mysql"
	"vitess.io/vitess/go/vt/sqlparser"

	"github.com/winebarrel/myschema/model"
	"github.com/winebarrel/myschema/parser"
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
	// LEFT JOIN information_schema.COLLATIONS so we get the canonical
	// CHARACTER_SET_NAME for the table's default collation. We could
	// derive the charset by stripping at the first underscore, but that
	// breaks for the "utf8" → "utf8mb3" alias and similar normalised
	// names — going through COLLATIONS is the only authoritative source.
	q := `
SELECT t.TABLE_SCHEMA, t.TABLE_NAME, t.ENGINE,
       t.TABLE_COLLATION, c.CHARACTER_SET_NAME, t.TABLE_COMMENT
FROM   information_schema.TABLES t
LEFT JOIN information_schema.COLLATIONS c
       ON c.COLLATION_NAME = t.TABLE_COLLATION
WHERE  t.TABLE_SCHEMA = ?
  AND  t.TABLE_TYPE = 'BASE TABLE'
ORDER  BY t.TABLE_NAME`

	rows, err := c.conn.QueryContext(ctx, q, c.database)
	if err != nil {
		return nil, fmt.Errorf("catalog: list tables: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	for rows.Next() {
		var (
			db, name      string
			engine        *string
			coll, charset *string
			comment       string
		)
		if err := rows.Scan(&db, &name, &engine, &coll, &charset, &comment); err != nil {
			return nil, fmt.Errorf("catalog: scan tables: %w", err)
		}
		t := &model.Table{
			Database:    db,
			Name:        name,
			Engine:      engine,
			Charset:     charset,
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

	// Symmetric to the column-level case: when MySQL stores a table
	// whose CREATE statement only spelled out DEFAULT CHARSET, it
	// fills in TABLE_COLLATION with the charset's default. The desired
	// side has Collation=nil in that case, so collapse the catalog
	// value to match before any per-table comparison.
	for _, t := range out.CollectValues() {
		t.Collation = model.CollapseDefaultCollation(t.Charset, t.Collation)
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
	if err := c.loadPartitions(ctx, out); err != nil {
		return nil, err
	}

	return out, nil
}

// loadPartitions populates `Partition` on every partitioned table in
// `out`. Two-phase to keep cost proportional to partitioned-table count
// rather than total-table count: first list partitioned tables via
// `information_schema.PARTITIONS` (one row per table is enough — DISTINCT
// drops the per-partition rows we don't need), then `SHOW CREATE TABLE`
// each and pull the `PARTITION BY …` clause out of the comment block
// MySQL emits.
//
// Going through `SHOW CREATE TABLE` rather than reconstructing the
// clause from `information_schema.PARTITIONS` saves the entire
// catalog-side AST builder — MySQL emits SQL that vitess re-parses
// straight back into the same `*sqlparser.PartitionOption` shape the
// desired-side parser produces, so both sides normalise through one
// formatter and compare bytewise. That's all v1 needs for
// `dump → plan` to come back clean.
func (c *Catalog) loadPartitions(ctx context.Context, tables *orderedmap.Map[string, *model.Table]) error {
	q := `
SELECT DISTINCT TABLE_SCHEMA, TABLE_NAME
FROM   information_schema.PARTITIONS
WHERE  TABLE_SCHEMA = ?
  AND  PARTITION_NAME IS NOT NULL`
	rows, err := c.conn.QueryContext(ctx, q, c.database)
	if err != nil {
		return fmt.Errorf("catalog: list partitioned tables: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	type ref struct{ db, name string }
	var partitioned []ref
	for rows.Next() {
		var r ref
		if err := rows.Scan(&r.db, &r.name); err != nil {
			return fmt.Errorf("catalog: scan partitioned tables: %w", err)
		}
		partitioned = append(partitioned, r)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("catalog: iterate partitioned tables: %w", err)
	}
	for _, r := range partitioned {
		t, ok := tables.GetOk(model.Ident(r.db, r.name))
		if !ok {
			// Table dropped between the two queries, or filtered out
			// upstream — nothing to attach to.
			continue
		}
		var name, ddl string
		row := c.conn.QueryRowContext(ctx, "SHOW CREATE TABLE "+model.Ident(r.db, r.name))
		if err := row.Scan(&name, &ddl); err != nil {
			return fmt.Errorf("catalog: SHOW CREATE TABLE %s: %w", t.FQTN(), err)
		}
		clause, err := parser.ExtractPartitionFromShowCreate(ddl)
		if err != nil {
			return fmt.Errorf("catalog: extract partition clause for %s: %w", t.FQTN(), err)
		}
		if clause == "" {
			// Listed in information_schema.PARTITIONS but SHOW CREATE
			// TABLE didn't emit a PARTITION BY block — shouldn't
			// happen on stock MySQL, but be defensive.
			continue
		}
		clauseCopy := clause
		t.Partition = &clauseCopy
	}
	return nil
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
		// MySQL fills in two layers of defaults on every column that
		// doesn't override them: (1) the table's DEFAULT CHARSET /
		// COLLATE propagates down, and (2) when the user spells out
		// only CHARACTER SET on a column, MySQL fills in COLLATION_NAME
		// with that charset's server-side default collation. Both
		// layers are invisible on the parser side (the desired SQL just
		// doesn't say anything), so leaving the catalog values intact
		// would make every column diff as "different".
		//
		// Normalise: a column-level CHARACTER SET that matches the table
		// default collapses to nil; a column-level COLLATION that
		// matches the table default OR the effective charset's default
		// collation collapses to nil. Explicit per-column overrides
		// survive.
		normCharset := nullIfMatchesTableDefault(charset, t.Charset)
		// Column collation goes through two collapses:
		//   1. matches the table default → inherited, drop to nil
		//   2. matches the effective charset's default collation
		//      (column-level CHARACTER SET if present, otherwise the
		//      table default) → MySQL implied it, drop to nil
		// Explicit per-column overrides survive.
		normColl := nullIfMatchesTableDefault(coll, t.Collation)
		effectiveCharset := charset
		if effectiveCharset == nil {
			effectiveCharset = t.Charset
		}
		normColl = model.CollapseDefaultCollation(effectiveCharset, normColl)
		col := &model.Column{
			Name:         name,
			TypeName:     strings.ToLower(colType),
			NotNull:      strings.EqualFold(isNullable, "NO"),
			Collation:    normColl,
			CharacterSet: normCharset,
		}
		if defVal != nil {
			d := normalizeColumnDefault(strings.ToLower(colType), *defVal)
			col.Default = &d
		}
		extraUp := strings.ToUpper(extra)
		// MySQL 8.0+ surfaces invisible columns as the literal token
		// `INVISIBLE` in EXTRA. It can appear *alongside* other extras
		// (e.g. `DEFAULT_GENERATED on update CURRENT_TIMESTAMP INVISIBLE`),
		// so detect it first and strip it from the working string —
		// otherwise the trailing INVISIBLE would leak into the ON UPDATE
		// TrimPrefix below as part of the expression value.
		if strings.Contains(extraUp, "INVISIBLE") {
			col.Invisible = true
			extraUp = strings.TrimSpace(strings.Replace(extraUp, "INVISIBLE", "", 1))
		}
		if strings.Contains(extraUp, "AUTO_INCREMENT") {
			col.AutoIncrement = true
		}
		if strings.Contains(extraUp, "ON UPDATE ") {
			ou := strings.TrimPrefix(extraUp, "DEFAULT_GENERATED ON UPDATE ")
			ou = strings.TrimPrefix(ou, "ON UPDATE ")
			col.OnUpdate = &ou
		}
		if genExpr != "" {
			ge := decodeGenerationExpr(genExpr)
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
	// (CONSTRAINT_TYPE = 'CHECK') to scope by table — and to also pick up
	// `tc.ENFORCED` ("YES" / "NO") so a desired-side `CHECK (...) NOT
	// ENFORCED` round-trips cleanly. Without ENFORCED in the read, the
	// catalog would always report Enforced=true and any NOT ENFORCED
	// constraint would re-fire DROP CHECK + ADD CONSTRAINT on every plan.
	q := `
SELECT cc.CONSTRAINT_NAME, cc.CHECK_CLAUSE, tc.ENFORCED
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
		var name, clause, enforced string
		if err := rows.Scan(&name, &clause, &enforced); err != nil {
			return fmt.Errorf("catalog: scan check constraints for %s: %w", t.FQTN(), err)
		}
		t.Constraints.Set(name, &model.Constraint{
			Name:       name,
			Type:       model.CheckConstraint,
			Definition: "CHECK (" + clause + ")",
			Enforced:   enforced == "YES",
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
// For non-empty defaults we delegate the classification to vitess: try
// to parse `SELECT <def>`, and if the resulting expression is a
// *ColName, MySQL stored it as a quoteless literal — wrap it. Anything
// else (Literal, NullVal, BoolVal, CurTimeFuncExpr, FuncExpr,
// BinaryExpr, …) is already valid SQL.
//
// The empty-string default is special-cased because vitess can't parse
// `SELECT` with an empty expression. The same quoted-vs-bare drift
// that this function wraps for non-empty barewords also bites for
// empty ones: the parser side stores the quoted empty literal (a
// pair of single-quote characters) while the catalog returns the
// bare empty string. For string-shaped column types where MySQL
// stores the empty default as the bare empty string in
// information_schema.COLUMNS.COLUMN_DEFAULT (CHAR / VARCHAR /
// VARBINARY / ENUM / SET) we wrap the bare empty string here so it
// matches the parser side. TEXT / BLOB are excluded because MySQL
// doesn't allow a literal default on those types at all.
//
// Fixed-width BINARY(N) takes a separate path: MySQL surfaces its
// empty default through information_schema as the literal two-
// character string "0x" (independent of N — BINARY(1), BINARY(4),
// and BINARY(16) all return the same sentinel). We recognise that
// sentinel below and rewrite it to the quoted empty literal so the
// round-trip closes.
func normalizeColumnDefault(typeName, def string) string {
	if def == "" {
		if columnTypeAllowsEmptyStringDefault(typeName) {
			return "''"
		}
		return def
	}
	if def == "0x" && strings.HasPrefix(typeName, "binary") {
		return "''"
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

// columnTypeAllowsEmptyStringDefault reports whether MySQL stores a
// literal empty default as the bare empty string in
// information_schema.COLUMNS.COLUMN_DEFAULT. typeName comes from
// COLUMN_TYPE, lowercased upstream — it includes the size suffix
// (e.g. `varchar(64)`) and modifiers, so we match by leading-token
// prefix rather than full equality.
//
// Fixed-width BINARY is excluded here because it doesn't follow the
// bare-empty-string rule: its empty default surfaces as the literal
// "0x" sentinel instead. That sentinel is handled directly in
// normalizeColumnDefault, so this function only needs to enumerate
// the types that take the bare-empty-string path.
func columnTypeAllowsEmptyStringDefault(typeName string) bool {
	// Fixed-width BINARY does not use the bare-empty-string path — see
	// function-doc. Check it first so the supported list below stays a
	// flat OR.
	if strings.HasPrefix(typeName, "binary") {
		return false
	}
	switch {
	case strings.HasPrefix(typeName, "varchar"),
		strings.HasPrefix(typeName, "char"),
		strings.HasPrefix(typeName, "varbinary"),
		strings.HasPrefix(typeName, "enum"),
		strings.HasPrefix(typeName, "set"):
		return true
	}
	return false
}

// nullIfMatchesTableDefault returns nil when the column-level value
// matches the table-level default; otherwise it passes the column-level
// pointer through unchanged. Either side being nil is treated as "no
// default to match" — only an explicit catalog value that matches the
// table default is collapsed.
func nullIfMatchesTableDefault(colVal, tableDefault *string) *string {
	if colVal == nil || tableDefault == nil {
		return colVal
	}
	if *colVal == *tableDefault {
		return nil
	}
	return colVal
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

// decodeGenerationExpr translates MySQL's escape encoding for the
// `information_schema.COLUMNS.GENERATION_EXPRESSION` field back into a
// vitess-parseable form. MySQL stores generated bodies with every
// `\` doubled to `\\` and every `'` (string-literal delimiter or
// content) preceded by `\`, so a source clause
// CONCAT(name, ' x ', name) round-trips as the literal byte
// sequence  concat(`name`,_utf8mb4\' x \',`name`) . vitess refuses the
// `\'`-as-delimiter form ("syntax error near '\\'"), and without
// decoding `equalExprPtr` falls back to byte equality and fires a
// no-op MODIFY COLUMN on every plan.
//
// Decoding is two passes: `\\` → `\` first (so a doubled backslash
// becomes one literal backslash), then `\'` → `'` (so an escaped
// apostrophe becomes a plain one). Order matters: doing `\'` first
// would over-double a literal `\` whose encoded form is `\\\\`.
//
// Verified against the three real-world shapes (probed against
// MySQL 8.0):
//
//	source body | encoded body | after two-pass decode
//	------------+--------------+----------------------
//	' x '       | \' x \'      | ' x '
//	'\\'        | \'\\\\\'     | '\\'
//	'\''        | \'\\\'\'     | '\''
//
// All three round-trip cleanly through vitess after decoding.
func decodeGenerationExpr(s string) string {
	s = strings.ReplaceAll(s, `\\`, `\`)
	s = strings.ReplaceAll(s, `\'`, `'`)
	return s
}
