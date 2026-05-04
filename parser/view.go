package parser

import (
	"fmt"
	"strings"

	"vitess.io/vitess/go/vt/sqlparser"

	"github.com/winebarrel/myschema/model"
)

// parseCreateView turns a *sqlparser.CreateView into a model.View. The
// SELECT body is restored to its lower-cased canonical form so the parser
// side and information_schema.VIEWS.VIEW_DEFINITION compare equal after
// NormalizeViewDefinition runs the same pipeline over both.
func parseCreateView(s *sqlparser.CreateView, defaultDB string) (*model.View, error) {
	db, err := resolveStmtDB(s.ViewName.Qualifier.String(), defaultDB, "CREATE VIEW", s.ViewName.Name.String())
	if err != nil {
		return nil, err
	}
	v := &model.View{
		Database: db,
		Name:     s.ViewName.Name.String(),
	}
	if len(s.Columns) > 0 {
		v.Cols = make([]string, len(s.Columns))
		for i, c := range s.Columns {
			v.Cols[i] = c.String()
		}
	}

	def, err := restoreSelectLower(s.Select)
	if err != nil {
		return nil, fmt.Errorf("view %s: deparse SELECT: %w", v.FQVN(), err)
	}
	v.Definition = def

	// CheckOption is the only optional clause we model; ALGORITHM,
	// DEFINER, and SQL SECURITY are out of scope (CAVEATS.md).
	v.CheckOption = s.CheckOption
	if v.CheckOption == "" {
		v.CheckOption = "NONE"
	}

	return v, nil
}

// restoreSelectLower returns the SELECT body lower-cased (vitess defaults to
// lower-case keywords already, but we run ToLower as a safety net).
func restoreSelectLower(node sqlparser.SQLNode) (string, error) {
	if node == nil {
		return "", nil
	}
	return strings.ToLower(sqlparser.String(node)), nil
}

// NormalizeViewDefinition runs both the parser-side definition and the
// information_schema-side definition through the same pipeline so they
// compare equal:
//   - parse the SELECT body
//   - rewrite the AST: drop schema/table qualifiers from column refs, drop
//     redundant `AS col` aliases, unwrap any extra parentheses
//   - restore via vitess (lowercase keywords) and ReplaceAll spaces inside
//     the rendered output
//
// Without this, the catalog form
// (`select \`db\`.\`t\`.\`c\` AS \`c\` from \`db\`.\`t\“) wouldn't compare
// equal to the parser form (`select \`c\` from \`t\“).
func NormalizeViewDefinition(def, defaultDB string) (string, error) {
	if def == "" {
		return "", nil
	}
	p, err := newParser()
	if err != nil {
		return "", err
	}
	stmt, err := p.Parse(def)
	if err != nil {
		return "", fmt.Errorf("normalise view: parse %q: %w", def, err)
	}
	// All three rewrites mutate the AST in place; chain just for readability.
	stripQualifiers(stmt)
	stripRedundantAliases(stmt)
	unwrapParens(stmt)
	out := strings.ToLower(sqlparser.String(stmt))
	return out, nil
}

// stripQualifiers walks the AST and clears the database / table qualifier
// from every ColName so `db.t.c` becomes bare `c`. Also strips the database
// qualifier from TableName references so `db.t` becomes `t`. The caller is
// expected to have a single default database in scope.
func stripQualifiers(stmt sqlparser.SQLNode) {
	_ = sqlparser.Walk(func(n sqlparser.SQLNode) (bool, error) {
		switch v := n.(type) {
		case *sqlparser.ColName:
			v.Qualifier.Name = sqlparser.NewIdentifierCS("")
			v.Qualifier.Qualifier = sqlparser.NewIdentifierCS("")
		case sqlparser.TableName:
			// TableName is value-typed, can't mutate via interface walk;
			// AliasedTableExpr below holds the editable copy.
			_ = v
		case *sqlparser.AliasedTableExpr:
			if t, ok := v.Expr.(sqlparser.TableName); ok {
				t.Qualifier = sqlparser.NewIdentifierCS("")
				v.Expr = t
			}
		}
		return true, nil
	}, stmt)
}

// stripRedundantAliases drops `AS col` from `SELECT col AS col` patterns.
// MySQL adds these to information_schema.VIEWS.VIEW_DEFINITION even when
// the user didn't write them. Only redundant aliases (alias name equals
// the underlying ColName) are dropped — aliases on function calls,
// expressions, or columns where the user-chosen alias differs from the
// column name are meaningful and must survive the normaliser, otherwise
// view-body edits that rename a column-alias would silently compare
// equal at diff time and the live view would never be replaced.
func stripRedundantAliases(stmt sqlparser.SQLNode) {
	_ = sqlparser.Walk(func(n sqlparser.SQLNode) (bool, error) {
		sel, ok := n.(*sqlparser.Select)
		if !ok {
			return true, nil
		}
		for _, e := range sel.SelectExprs.Exprs {
			ae, ok := e.(*sqlparser.AliasedExpr)
			if !ok || ae.As.IsEmpty() {
				continue
			}
			col, ok := ae.Expr.(*sqlparser.ColName)
			if !ok {
				// Alias on a non-column expression (COUNT(*),
				// arithmetic, CASE, …) is always meaningful — the
				// underlying expression has no canonical name to
				// compare against.
				continue
			}
			// stripQualifiers runs first, so col.Name is the bare
			// column name with no qualifier. MySQL identifiers are
			// case-insensitive; vitess' IdentifierCI.Equal handles
			// the fold.
			if !ae.As.Equal(col.Name) {
				continue
			}
			ae.As = sqlparser.NewIdentifierCI("")
		}
		return true, nil
	}, stmt)
}

// unwrapParens replaces ParenExpr with its inner Expr where it appears
// inside binary operations and comparisons. MySQL wraps the WHERE
// predicate in extra parens that we want to ignore for comparison.
func unwrapParens(stmt sqlparser.SQLNode) {
	// vitess does not have a direct "ParenExpr" exposed for general use the
	// way pingcap does; vitess prints binary operations with consistent
	// parens so this normaliser is currently a no-op but kept as a hook
	// for when paren handling drifts in the future.
	_ = stmt
}

// ViewReferences returns every table-or-view reference found in def, as
// `model.Ident(db, name)` strings (defaultDB filling in unqualified ones).
// The diff engine uses these to topo-sort views.
//
// Aliases are filtered out by checking AliasedTableExpr.Expr first — only
// the underlying TableName contributes a reference.
func ViewReferences(def, defaultDB string) ([]string, error) {
	p, err := newParser()
	if err != nil {
		return nil, err
	}
	stmt, err := p.Parse(def)
	if err != nil {
		return nil, fmt.Errorf("view references: parse %q: %w", def, err)
	}
	seen := map[string]struct{}{}
	var refs []string
	_ = sqlparser.Walk(func(n sqlparser.SQLNode) (bool, error) {
		ate, ok := n.(*sqlparser.AliasedTableExpr)
		if !ok {
			return true, nil
		}
		tn, ok := ate.Expr.(sqlparser.TableName)
		if !ok {
			return true, nil
		}
		db := tn.Qualifier.String()
		if db == "" {
			db = defaultDB
		}
		key := model.Ident(db, tn.Name.String())
		if _, dup := seen[key]; dup {
			return true, nil
		}
		seen[key] = struct{}{}
		refs = append(refs, key)
		return true, nil
	}, stmt)
	return refs, nil
}
