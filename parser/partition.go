package parser

import (
	"fmt"
	"regexp"
	"strings"

	"vitess.io/vitess/go/vt/sqlparser"
)

// partitionPerDefinitionEngineRe strips the per-PARTITION `engine
// <name>` option that vitess emits when MySQL's SHOW CREATE TABLE
// includes `ENGINE = InnoDB` on each partition. User-written desired
// SQL almost never spells the per-partition engine out, so leaving it
// in would surface as drift on every plan. The match is bounded by
// either a comma (more partitions follow) or `)` (end of partition
// list) so we don't accidentally chew into the next clause.
var partitionPerDefinitionEngineRe = regexp.MustCompile(`(?i)\s+engine\s+\w+(?P<tail>[,)])`)

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
	s := strings.TrimLeft(sqlparser.String(po), "\n")
	s = partitionPerDefinitionEngineRe.ReplaceAllString(s, "$tail")
	return s
}

// ExtractPartitionFromShowCreate isolates the `PARTITION BY …` clause
// (and the parenthesised partition definitions) from a
// `SHOW CREATE TABLE` result and returns it normalised through the
// same vitess pipeline NormalizePartitionOption uses. Returns
// ("", nil) when the table is not partitioned. A catalog-side table
// that uses `SUBPARTITION BY …` is rejected with an error
// (SUBPARTITION is intentionally out of scope for v1 — see
// CAVEATS.md "Partitioning is round-trip only in v1") so myschema
// won't try to manage it.
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
