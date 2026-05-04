package parser

import (
	"fmt"
	"strings"

	"vitess.io/vitess/go/vt/sqlparser"
)

// stripPartitionEngineOptions clears the per-partition `ENGINE =
// <name>` option from a PartitionOption AST, in place, before the
// caller hands the AST to vitess's formatter. MySQL 8.0+ always
// emits `ENGINE = InnoDB` on every partition in SHOW CREATE TABLE
// output even when the table-level engine alone would suffice;
// user-written desired SQL almost never spells the per-partition
// engine out, so leaving it in would surface as drift on every
// plan. We strip at the AST level so we don't have to reason about
// quoting in the formatted SQL — an earlier regex-based approach
// got that wrong for LIST values like `'foo engine innodb'`.
func stripPartitionEngineOptions(po *sqlparser.PartitionOption) {
	for _, def := range po.Definitions {
		if def.Options != nil {
			def.Options.Engine = nil
		}
	}
}

// NormalizePartitionOption renders a vitess `*sqlparser.PartitionOption`
// into the canonical SQL form myschema stores on `model.Table.Partition`.
// Both the parser side (desired-SQL) and the catalog side (`SHOW CREATE
// TABLE` re-parse) call through here so the two sides compare bytewise
// — this is the only reason `dump → plan` reports no diff for a
// partitioned table.
//
// Three normalisations on top of vitess's own formatter:
//   - function names and column references inside the partition
//     expression are lower-cased. Vitess preserves the user's
//     casing (`YEAR(dt)`) but MySQL 8.0+ always emits its own
//     output in lower case (`year(dt)`); without this pass the
//     two sides would differ on every plan. SQL is case-
//     insensitive for both kinds of identifier, so lower-casing
//     is safe — and string literals (the LIST `VALUES IN ('A',
//     'B')` case) are untouched because they're not column
//     references or function names in the AST.
//   - the leading newline is trimmed so the value can be
//     concatenated onto a CREATE TABLE by `(*model.Table).SQL`
//     without producing a blank line.
//   - per-partition `engine <name>` options are stripped so a
//     desired-side CREATE TABLE that doesn't spell out the per-
//     partition engine still matches a catalog SHOW CREATE TABLE
//     that does (MySQL 8.0+ always emits `ENGINE = InnoDB` per
//     partition even when the table-level engine alone would
//     suffice).
func NormalizePartitionOption(po *sqlparser.PartitionOption) string {
	sqlparser.Rewrite(po, func(c *sqlparser.Cursor) bool {
		switch n := c.Node().(type) {
		case *sqlparser.FuncExpr:
			n.Name = sqlparser.NewIdentifierCI(strings.ToLower(n.Name.String()))
		case *sqlparser.ColName:
			n.Name = sqlparser.NewIdentifierCI(strings.ToLower(n.Name.String()))
		}
		return true
	}, nil)
	stripPartitionEngineOptions(po)
	return strings.TrimLeft(sqlparser.String(po), "\n")
}

// ExtractPartitionFromShowCreate isolates the `PARTITION BY …` clause
// (and the parenthesised partition definitions) from a
// `SHOW CREATE TABLE` result and returns it normalised through the
// same vitess pipeline NormalizePartitionOption uses. Returns
// ("", nil) when the table is not partitioned. A catalog-side table
// that uses `SUBPARTITION BY …` is rejected with an error
// (SUBPARTITION is intentionally out of scope for v1 — see
// PARTITIONING.md "SUBPARTITION") so myschema won't try to
// manage it.
//
// MySQL wraps the partition clause in a versioned comment like
// `/*!50100 PARTITION BY RANGE … */`. SHOW CREATE TABLE may also
// include other versioned blocks (e.g. `/*!50100 TABLESPACE …`,
// `/*!80016 ENCRYPTION='N' */`) earlier in the output, so this
// function searches from the *end* — the partition block, when
// present, is always the trailing one MySQL appends. Once the
// last block is located the surrounding `/*!50100 ` and `*/` are
// stripped, the result is glued onto a trivial
// `CREATE TABLE _ (id INT)` skeleton, and vitess re-parses so
// the format normalisation matches the desired-SQL path.
func ExtractPartitionFromShowCreate(showCreate string) (string, error) {
	idx := strings.LastIndex(showCreate, "/*!")
	if idx < 0 {
		// No versioned-comment block at all → not partitioned.
		return "", nil
	}
	// `*/` after the last `/*!` closes that block. Search from `idx`
	// onwards (not the global LastIndex) to keep this resilient to
	// hypothetical `*/` inside a string literal earlier in the output
	// — the trailing partition block is the only thing we care about.
	end := strings.Index(showCreate[idx:], "*/")
	if end < 0 {
		return "", fmt.Errorf("malformed SHOW CREATE TABLE output: trailing /*! without closing */")
	}
	end += idx
	body := showCreate[idx+3 : end]
	// `/*!50100 PARTITION BY …` — drop the leading version digits so
	// only the SQL keywords remain.
	body = strings.TrimLeft(body, "0123456789")
	body = strings.TrimSpace(body)
	if !strings.HasPrefix(strings.ToUpper(body), "PARTITION BY") {
		// Trailing versioned block is for something else (rare on
		// stock MySQL but possible — e.g. an extension that appends
		// its own `/*!` comment). If the trailing block isn't a
		// partition clause the table isn't partitioned by anything
		// myschema models.
		return "", nil
	}
	p, err := newParser()
	if err != nil {
		return "", fmt.Errorf("init vitess parser: %w", err)
	}
	stmt, err := p.Parse("CREATE TABLE _t (id INT) " + body)
	if err != nil {
		return "", fmt.Errorf("re-parse partition clause %q: %w", body, err)
	}
	ct, ok := stmt.(*sqlparser.CreateTable)
	if !ok || ct.TableSpec == nil || ct.TableSpec.PartitionOption == nil {
		return "", fmt.Errorf("re-parsed partition clause produced no PartitionOption: %q", body)
	}
	if ct.TableSpec.PartitionOption.SubPartition != nil {
		return "", fmt.Errorf("SUBPARTITION BY is out of scope for v1 partition support; this catalog table cannot be managed by myschema until the SUBPARTITION clause is removed")
	}
	return NormalizePartitionOption(ct.TableSpec.PartitionOption), nil
}

// ParsePartitionClause re-parses a normalised partition clause (the
// kind stored on `model.Table.Partition`) back into a
// `*sqlparser.PartitionOption` so the diff layer can inspect
// individual partition definitions. The same trick used elsewhere:
// glue the clause onto a `CREATE TABLE _ (id INT)` skeleton and
// peel the AST back out.
//
// Returns nil when the input is empty (the model represents a
// non-partitioned table).
func ParsePartitionClause(clause string) (*sqlparser.PartitionOption, error) {
	if clause == "" {
		return nil, nil
	}
	p, err := newParser()
	if err != nil {
		return nil, fmt.Errorf("init vitess parser: %w", err)
	}
	stmt, err := p.Parse("CREATE TABLE _t (id INT) " + clause)
	if err != nil {
		return nil, fmt.Errorf("re-parse partition clause %q: %w", clause, err)
	}
	ct, ok := stmt.(*sqlparser.CreateTable)
	if !ok || ct.TableSpec == nil || ct.TableSpec.PartitionOption == nil {
		return nil, fmt.Errorf("re-parsed partition clause produced no PartitionOption: %q", clause)
	}
	return ct.TableSpec.PartitionOption, nil
}

// FormatPartitionDefinition renders a single
// `*sqlparser.PartitionDefinition` back into SQL, with the same
// per-partition `engine <name>` stripping NormalizePartitionOption
// performs at whole-clause level. Used by the diff layer to build
// `ALTER TABLE … ADD PARTITION (PARTITION p VALUES …)` statements.
//
// We mutate def.Options.Engine to nil before formatting (and
// restore it after) so that string-quoting in the rendered SQL
// never matters — earlier regex-based stripping mangled LIST
// values like `'foo engine innodb'`.
func FormatPartitionDefinition(def *sqlparser.PartitionDefinition) string {
	var savedEngine *sqlparser.PartitionEngine
	if def.Options != nil {
		savedEngine = def.Options.Engine
		def.Options.Engine = nil
		defer func() { def.Options.Engine = savedEngine }()
	}
	return sqlparser.String(def)
}
