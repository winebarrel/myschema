package diff

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/winebarrel/myschema/parser"
)

// regression: formatExpr must NOT lower-case string literals — that
// used to silently equate `WHEN region='US'` and `WHEN region='us'`,
// missing a real expression change. The header equality goes through
// vitess's formatter on AST trees that were already identifier-
// normalised by parser.NormalizePartitionOption, so two partition
// expressions that differ only in literal case must produce
// different formatted strings and trip the strategy / expression
// error.
//
// We feed two cleansed clauses through the same code path
// diffPartitions takes (parser.ParsePartitionClause +
// partitionHeaderEqual). RANGE expression is forced through a
// CASE-with-string-literal so the lower-case bug surfaces.
func TestPartitionHeaderEqualPreservesStringLiteralCase(t *testing.T) {
	// We have to use clauses that re-parse cleanly through
	// `CREATE TABLE _t (id INT) <clause>`, so build minimal RANGE
	// shapes whose Expr contains a string literal. CASE WHEN is
	// the most direct way.
	upper := "partition by range (case when region = 'US' then 1 else 2 end) (partition p0 values less than (10))"
	lower := "partition by range (case when region = 'us' then 1 else 2 end) (partition p0 values less than (10))"

	upperPO, err := parser.ParsePartitionClause(upper)
	require.NoError(t, err)
	lowerPO, err := parser.ParsePartitionClause(lower)
	require.NoError(t, err)

	assert.False(t, partitionHeaderEqual(upperPO, lowerPO),
		"string literal case must NOT be folded — `'US'` and `'us'` are different values, the diff must catch them")
}

// regression: DROP PARTITION must quote partition names through
// model.Ident so reserved-word names don't produce invalid SQL.
func TestDiffPartitionsQuotesReservedPartitionNames(t *testing.T) {
	// Catalog has a partition literally named `select` (a reserved
	// word). desired drops it. Output must back-tick the name.
	cur := stringPtr("partition by range (id) (partition p0 values less than (10), partition `select` values less than (20))")
	des := stringPtr("partition by range (id) (partition p0 values less than (10))")

	stmts, disallowed, err := diffPartitions("db.t", cur, des, AllowAll{})
	require.NoError(t, err)
	require.Empty(t, disallowed)
	require.Len(t, stmts, 1)
	assert.True(t,
		strings.Contains(stmts[0], "DROP PARTITION `select`"),
		"DROP PARTITION must back-tick reserved-word names; got %q", stmts[0])
}

// regression: ADD PARTITION must round-trip a partition whose name
// is a MySQL reserved word. vitess's PartitionDefinition formatter
// is supposed to back-tick those automatically; the DROP path
// goes through model.Ident explicitly (already pinned by
// TestDiffPartitionsQuotesReservedPartitionNames). This test
// keeps the ADD path honest in case the upstream formatter
// changes behaviour.
func TestDiffPartitionsQuotesReservedPartitionNamesOnAdd(t *testing.T) {
	cur := stringPtr("partition by range (id) (partition p0 values less than (10))")
	des := stringPtr("partition by range (id) (partition p0 values less than (10), partition `select` values less than (20))")

	stmts, disallowed, err := diffPartitions("db.t", cur, des, AllowAll{})
	require.NoError(t, err)
	require.Empty(t, disallowed)
	require.Len(t, stmts, 1)
	assert.True(t,
		strings.Contains(stmts[0], "`select`"),
		"ADD PARTITION must back-tick reserved-word names; got %q", stmts[0])
}

// regression: KEY partitions where the catalog and desired strings
// only differ in identifier casing (or whitespace, etc. that vitess
// normalises away on re-parse) used to surface as the
// "HASH / KEY partition diffs are not yet generated" error — the
// raw-string fast path saw a byte-difference, then partitionHeaderEqual
// lower-cased ColList and reported "headers equal", and the HASH/KEY
// branch unconditionally returned the unsupported-diff error. Equal
// `Partitions` count plus equal header should return no changes.
func TestDiffPartitionsKEYNoOpAcrossIdentifierCasing(t *testing.T) {
	cur := stringPtr("partition by key (userid) partitions 4")
	des := stringPtr("partition by key (UserID) partitions 4") // same column, different casing

	stmts, disallowed, err := diffPartitions("db.t", cur, des, AllowAll{})
	require.NoError(t, err)
	assert.Empty(t, stmts, "no executable statements expected for a KEY no-op")
	assert.Empty(t, disallowed, "no skipped statements expected for a KEY no-op")
}

func TestDiffPartitionsKEYCountChangeStillErrors(t *testing.T) {
	// Counterpart to the no-op case: when the count actually changes
	// the HASH/KEY branch must still surface the unsupported-diff
	// error so the user knows to run COALESCE / ADD PARTITION
	// PARTITIONS by hand.
	cur := stringPtr("partition by key (user_id) partitions 2")
	des := stringPtr("partition by key (user_id) partitions 4")

	_, _, err := diffPartitions("db.t", cur, des, AllowAll{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HASH / KEY partition diffs are not yet generated")
}

func stringPtr(s string) *string { return &s }
