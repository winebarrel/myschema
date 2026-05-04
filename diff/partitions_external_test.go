package diff_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"vitess.io/vitess/go/vt/sqlparser"

	"github.com/winebarrel/myschema/diff"
	"github.com/winebarrel/myschema/parser"
)

// firstPartition re-parses a partition clause and returns the first
// PartitionDefinition for assertion-friendly direct access.
func firstPartition(t *testing.T, clause string) *sqlparser.PartitionDefinition {
	t.Helper()
	po, err := parser.ParsePartitionClause(clause)
	require.NoError(t, err)
	require.NotEmpty(t, po.Definitions)
	return po.Definitions[0]
}

func TestTemporarilySortListValues_NoOpWhenOptionsMissing(t *testing.T) {
	// RANGE LESS THAN partitions have Options.ValueRange but it's of
	// LessThanType, not InType — nothing to sort, restore must be nil.
	a := firstPartition(t,
		"PARTITION BY RANGE (id) (PARTITION p1 VALUES LESS THAN (10))")
	b := firstPartition(t,
		"PARTITION BY RANGE (id) (PARTITION p1 VALUES LESS THAN (10))")

	// Strip Options on the right to hit the `Options == nil` early-out.
	bNoOptions := *b
	bNoOptions.Options = nil
	restore := diff.TemporarilySortListValues(a, &bNoOptions)
	assert.Nil(t, restore, "Options nil → no sort, no restore")
}

func TestTemporarilySortListValues_NoOpWhenValueRangeMissing(t *testing.T) {
	// `Options.ValueRange` nil triggers the second arm of the early-out.
	a := firstPartition(t,
		"PARTITION BY RANGE (id) (PARTITION p1 VALUES LESS THAN (10))")
	bare := *a
	bare.Options = &sqlparser.PartitionDefinitionOptions{} // Options set, but no ValueRange
	restore := diff.TemporarilySortListValues(a, &bare)
	assert.Nil(t, restore, "ValueRange nil → no sort, no restore")
}

func TestPartitionValueRangeEqual(t *testing.T) {
	t.Run("both Options nil compare equal", func(t *testing.T) {
		// Synthesise two empty PartitionDefinitions: nil Options on both
		// sides means the early `ar == nil && br == nil` branch returns
		// true.
		empty := &sqlparser.PartitionDefinition{}
		assert.True(t, diff.PartitionValueRangeEqual(empty, empty))
	})

	t.Run("one side has ValueRange, other doesn't → not equal", func(t *testing.T) {
		rng := firstPartition(t,
			"PARTITION BY RANGE (id) (PARTITION p1 VALUES LESS THAN (10))")
		empty := &sqlparser.PartitionDefinition{}
		assert.False(t, diff.PartitionValueRangeEqual(empty, rng))
		assert.False(t, diff.PartitionValueRangeEqual(rng, empty))
	})

	t.Run("identical RANGE LESS THAN compare equal", func(t *testing.T) {
		a := firstPartition(t,
			"PARTITION BY RANGE (id) (PARTITION p1 VALUES LESS THAN (10))")
		b := firstPartition(t,
			"PARTITION BY RANGE (id) (PARTITION p1 VALUES LESS THAN (10))")
		assert.True(t, diff.PartitionValueRangeEqual(a, b))
	})

	t.Run("LIST IN with reordered values fold to equal (set semantics)", func(t *testing.T) {
		a := firstPartition(t,
			"PARTITION BY LIST (n) (PARTITION p VALUES IN (1, 2, 3))")
		b := firstPartition(t,
			"PARTITION BY LIST (n) (PARTITION p VALUES IN (3, 1, 2))")
		assert.True(t, diff.PartitionValueRangeEqual(a, b))
	})
}

func TestPartitionHeaderEqual_NilCases(t *testing.T) {
	// Both nil compare equal; one nil, one not → unequal.
	assert.True(t, diff.PartitionHeaderEqual(nil, nil))

	po, err := parser.ParsePartitionClause("PARTITION BY HASH (id) PARTITIONS 4")
	require.NoError(t, err)
	assert.False(t, diff.PartitionHeaderEqual(po, nil))
	assert.False(t, diff.PartitionHeaderEqual(nil, po))
}

func TestRangeBoundaryAsInt64(t *testing.T) {
	// Pure helper. Three branches:
	// (1) single literal IntVal → returns the value, true
	// (2) tuple boundary (RANGE COLUMNS shape) → 0, false
	// (3) non-Literal expression → 0, false
	t.Run("single int literal", func(t *testing.T) {
		def := firstPartition(t,
			"PARTITION BY RANGE (id) (PARTITION p VALUES LESS THAN (42))")
		v, ok := diff.RangeBoundaryAsInt64(def.Options.ValueRange)
		assert.True(t, ok)
		assert.Equal(t, int64(42), v)
	})
	t.Run("tuple boundary (RANGE COLUMNS) returns 0,false", func(t *testing.T) {
		def := firstPartition(t,
			"PARTITION BY RANGE COLUMNS (a, b) (PARTITION p VALUES LESS THAN (10, 20))")
		v, ok := diff.RangeBoundaryAsInt64(def.Options.ValueRange)
		assert.False(t, ok, "len(Range) != 1 → not a single-int boundary")
		assert.Equal(t, int64(0), v)
	})
	t.Run("non-literal expression returns 0,false", func(t *testing.T) {
		// `TO_DAYS('2026-01-01')` is a function call, not a Literal.
		def := firstPartition(t,
			"PARTITION BY RANGE (TO_DAYS(d)) (PARTITION p VALUES LESS THAN (TO_DAYS('2026-01-01')))")
		v, ok := diff.RangeBoundaryAsInt64(def.Options.ValueRange)
		assert.False(t, ok, "function call isn't a Literal")
		assert.Equal(t, int64(0), v)
	})
}
