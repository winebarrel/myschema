package parser

import (
	"bytes"
	"fmt"

	tidbparser "github.com/pingcap/tidb/pkg/parser"
	"github.com/pingcap/tidb/pkg/parser/ast"
	"github.com/pingcap/tidb/pkg/parser/format"
	"github.com/winebarrel/myschema/model"
)

// parseCreateView turns a *ast.CreateViewStmt into a model.View. The SELECT
// body is restored to a canonical form so it round-trips against
// information_schema.VIEWS.VIEW_DEFINITION; see RestoreView for the
// normalisation logic.
func parseCreateView(s *ast.CreateViewStmt, defaultDB string) (*model.View, error) {
	v := &model.View{
		Database: dbName(s.ViewName.Schema.O, defaultDB),
		Name:     s.ViewName.Name.O,
	}

	if len(s.Cols) > 0 {
		v.Cols = make([]string, len(s.Cols))
		for i, c := range s.Cols {
			v.Cols[i] = c.O
		}
	}

	def, err := RestoreView(s.Select, defaultDB)
	if err != nil {
		return nil, fmt.Errorf("view %s: deparse SELECT: %w", v.FQVN(), err)
	}
	v.Definition = def

	v.Algorithm = s.Algorithm.String()
	v.Security = s.Security.String()

	// pingcap's AST cannot distinguish "no WITH CHECK OPTION clause" from
	// "WITH CASCADED CHECK OPTION" — both leave CheckOption at the AST
	// default (CheckOptionCascaded). The catalog side reports NONE for
	// the "no clause" case, so we collapse CASCADED → NONE at parse time.
	// Users who really need CASCADED-vs-NONE fidelity hit the limitation
	// documented in TODO.md.
	checkOpt := s.CheckOption.String()
	if checkOpt == "CASCADED" {
		checkOpt = "NONE"
	}
	v.CheckOption = checkOpt

	return v, nil
}

// RestoreView restores a SELECT body in the same canonical form
// information_schema.VIEWS.VIEW_DEFINITION uses, so the parser side and
// the catalog side compare equal after normalisation:
//
//   - lowercase keywords
//   - back-tick-quoted identifiers
//   - implicit table-name database qualification (i.e. `FROM users` becomes
//     `FROM `db`.`users“ when the default database is `db`).
//
// Column references are not re-qualified — MySQL's catalog adds full
// `db`.`table`.`column` forms, but matching that requires a full resolver
// here; instead, NormalizeViewDefinition strips that qualification on the
// catalog side.
func RestoreView(node ast.StmtNode, defaultDB string) (string, error) {
	var buf bytes.Buffer
	ctx := format.NewRestoreCtx(format.RestoreNameBackQuotes|format.RestoreKeyWordLowercase|format.RestoreStringSingleQuotes, &buf)
	ctx.DefaultDB = defaultDB
	if err := node.Restore(ctx); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// NormalizeViewDefinition takes a SELECT body (as produced by either parser
// restore or information_schema.VIEWS) and produces a canonical form so
// that the catalog's verbose form
// (`select \`db\`.\`t\`.\`c\` AS \`c\` from \`db\`.\`t\“) compares equal
// to the parser's terse form (`select \`c\` from \`t\“).
//
// Steps: re-parse → strip redundant `SELECT col AS col` aliases via an AST
// visitor → restore with RestoreWithoutSchemaName + RestoreWithoutTableName
// so column refs collapse to bare names on both sides.
func NormalizeViewDefinition(def, defaultDB string) (string, error) {
	p := tidbparser.New()
	stmts, _, err := p.Parse(def, "", "")
	if err != nil {
		return "", fmt.Errorf("normalise view: parse %q: %w", def, err)
	}
	if len(stmts) != 1 {
		return "", fmt.Errorf("normalise view: expected 1 statement, got %d", len(stmts))
	}
	stmts[0].Accept(&stripAllAliases{})
	stmts[0].Accept(&unwrapParens{})

	var buf bytes.Buffer
	flags := format.RestoreNameBackQuotes |
		format.RestoreKeyWordLowercase |
		format.RestoreStringSingleQuotes |
		format.RestoreWithoutSchemaName |
		format.RestoreWithoutTableName |
		format.RestoreSpacesAroundBinaryOperation |
		format.RestoreBracketAroundBinaryOperation
	ctx := format.NewRestoreCtx(flags, &buf)
	ctx.DefaultDB = defaultDB
	if err := stmts[0].Restore(ctx); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// stripAllAliases drops the AsName from every SelectField. MySQL stores
// `AS \`label\“ for every projected column in
// information_schema.VIEWS.VIEW_DEFINITION, so the comparison-side
// normalisation drops them on both sides. Equality of view definitions
// here is intentionally lossy: a real semantic diff (e.g. detecting that
// the user changed the AsName) is left to a future iteration. Implements
// ast.Visitor.
type stripAllAliases struct{}

func (stripAllAliases) Enter(n ast.Node) (ast.Node, bool) {
	if sf, ok := n.(*ast.SelectField); ok {
		sf.AsName.O = ""
		sf.AsName.L = ""
	}
	return n, false
}

func (stripAllAliases) Leave(n ast.Node) (ast.Node, bool) { return n, true }

// unwrapParens replaces every ParenthesesExpr with its inner expression.
// MySQL's catalog form wraps the WHERE predicate in parens that the user
// never wrote, so equality keeps tripping over the extra layer otherwise.
// Implements ast.Visitor; returns the inner expr from Leave so the parent
// installs it in place of the ParenthesesExpr.
type unwrapParens struct{}

func (unwrapParens) Enter(n ast.Node) (ast.Node, bool) { return n, false }

func (unwrapParens) Leave(n ast.Node) (ast.Node, bool) {
	if pe, ok := n.(*ast.ParenthesesExpr); ok && pe.Expr != nil {
		return pe.Expr, true
	}
	return n, true
}
