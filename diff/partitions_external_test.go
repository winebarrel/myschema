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
