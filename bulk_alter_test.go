package myschema

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestCombineSameTableAlters_NilAndSingle: edge cases must short-circuit
// without surprising the caller (no allocation, no transformation).
func TestCombineSameTableAlters_NilAndSingle(t *testing.T) {
	assert.Nil(t, combineSameTableAlters(nil))

	in := []string{"ALTER TABLE t ADD COLUMN c INT;"}
	got := combineSameTableAlters(in)
	assert.Equal(t, in, got)
}

// TestCombineSameTableAlters_TwoSameTable: the headline behaviour —
// two consecutive ALTERs on the same table fold into one multi-spec
// ALTER, separating specs with `, `. The trailing `;` from the input
// is preserved.
func TestCombineSameTableAlters_TwoSameTable(t *testing.T) {
	in := []string{
		"ALTER TABLE t ADD COLUMN a INT;",
		"ALTER TABLE t ADD COLUMN b INT;",
	}
	got := combineSameTableAlters(in)
	assert.Equal(t, []string{
		"ALTER TABLE t ADD COLUMN a INT, ADD COLUMN b INT;",
	}, got)
}

// TestCombineSameTableAlters_ThreeOrMoreCombined: combine works
// transitively, joining 3+ same-table ALTERs into one. Mixed spec
// kinds (ADD / MODIFY / DROP COLUMN) all merge — the combiner is
// spec-agnostic, only the table identifier matters.
func TestCombineSameTableAlters_ThreeOrMoreCombined(t *testing.T) {
	in := []string{
		"ALTER TABLE t ADD COLUMN a INT;",
		"ALTER TABLE t MODIFY COLUMN b VARCHAR(10);",
		"ALTER TABLE t DROP COLUMN c;",
	}
	got := combineSameTableAlters(in)
	assert.Equal(t, []string{
		"ALTER TABLE t ADD COLUMN a INT, MODIFY COLUMN b VARCHAR(10), DROP COLUMN c;",
	}, got)
}

// TestCombineSameTableAlters_DifferentTablesNotCombined: two ALTERs
// targeting different tables stay separate. Order is preserved.
func TestCombineSameTableAlters_DifferentTablesNotCombined(t *testing.T) {
	in := []string{
		"ALTER TABLE a ADD COLUMN c INT;",
		"ALTER TABLE b ADD COLUMN c INT;",
	}
	got := combineSameTableAlters(in)
	assert.Equal(t, in, got)
}

// TestCombineSameTableAlters_NonContiguousNotMerged: combine is
// CONSECUTIVE-only — it never reorders. With `a, b, a`, the first
// and third stay separate so callers can rely on the diff order
// (which encodes ordering invariants like FK drop → table change →
// FK add). A reorder-and-merge variant would change semantics; that's
// not what `--bulk-alter` promises.
func TestCombineSameTableAlters_NonContiguousNotMerged(t *testing.T) {
	in := []string{
		"ALTER TABLE a ADD COLUMN c INT;",
		"ALTER TABLE b ADD COLUMN c INT;",
		"ALTER TABLE a ADD COLUMN d INT;",
	}
	got := combineSameTableAlters(in)
	assert.Equal(t, in, got)
}

// TestCombineSameTableAlters_PartitionOpsPassThrough: partition ops
// (REORGANIZE / ADD / DROP / COALESCE / TRUNCATE / EXCHANGE PARTITION)
// have grammar restrictions that reject the trailing-comma multi-spec
// form. Detect via partitionOpInsertPos and pass them through unchanged;
// they also break any in-progress combine group on the same table.
func TestCombineSameTableAlters_PartitionOpsPassThrough(t *testing.T) {
	in := []string{
		"ALTER TABLE t ADD COLUMN a INT;",
		"ALTER TABLE t ADD PARTITION (PARTITION p1 VALUES LESS THAN (10));",
		"ALTER TABLE t ADD COLUMN b INT;",
	}
	got := combineSameTableAlters(in)
	assert.Equal(t, in, got)
}

// TestCombineSameTableAlters_CreateIndexPassThrough: standalone
// `CREATE INDEX … ON t (…);` isn't an `ALTER TABLE` statement; pass
// through unchanged. It also breaks an in-progress combine group on
// the same table because the runs separator is "any non-combinable
// statement," not just same-table.
func TestCombineSameTableAlters_CreateIndexPassThrough(t *testing.T) {
	in := []string{
		"ALTER TABLE t ADD COLUMN a INT;",
		"CREATE INDEX idx ON t (a);",
		"ALTER TABLE t ADD COLUMN b INT;",
	}
	got := combineSameTableAlters(in)
	assert.Equal(t, in, got)
}

// TestCombineSameTableAlters_BackticksPreserved: identifiers come from
// the diff layer, which routes them through model.Ident (back-ticks any
// risky name). Pin that the combiner compares identifier text verbatim:
// a back-ticked identifier merges with another back-ticked one, but
// would NOT merge with the same name unquoted (mismatched text). In
// practice the diff layer is consistent per table, so this case is
// defensive.
func TestCombineSameTableAlters_BackticksPreserved(t *testing.T) {
	in := []string{
		"ALTER TABLE `t` ADD COLUMN a INT;",
		"ALTER TABLE `t` ADD COLUMN b INT;",
	}
	got := combineSameTableAlters(in)
	assert.Equal(t, []string{
		"ALTER TABLE `t` ADD COLUMN a INT, ADD COLUMN b INT;",
	}, got)
}

// TestCombineSameTableAlters_NoSemicolonInInput: defence-in-depth.
// The diff layer always emits a trailing `;`, but pin the combiner's
// behaviour for callers that strip it: no `;` in → no `;` out.
func TestCombineSameTableAlters_NoSemicolonInInput(t *testing.T) {
	in := []string{
		"ALTER TABLE t ADD COLUMN a INT",
		"ALTER TABLE t ADD COLUMN b INT",
	}
	got := combineSameTableAlters(in)
	assert.Equal(t, []string{"ALTER TABLE t ADD COLUMN a INT, ADD COLUMN b INT"}, got)
}

// TestCombineSameTableAlters_MalformedAlterPassThrough: defence-in-depth.
// The diff layer never emits these shapes, but pin that splitCombinableAlter
// rejects them so a future caller can't accidentally combine garbage.
//
//   - "ALTER TABLE ;" / "ALTER TABLE  " — no identifier after the prefix
//   - "ALTER TABLE t;" / "ALTER TABLE t" — no spec body
//
// Each is treated as non-combinable and passes through verbatim, breaking
// any in-progress group.
func TestCombineSameTableAlters_MalformedAlterPassThrough(t *testing.T) {
	cases := [][]string{
		{
			"ALTER TABLE t ADD COLUMN a INT;",
			"ALTER TABLE ;", // missing identifier
			"ALTER TABLE t ADD COLUMN b INT;",
		},
		{
			"ALTER TABLE t ADD COLUMN a INT;",
			"ALTER TABLE t;", // missing spec body
			"ALTER TABLE t ADD COLUMN b INT;",
		},
	}
	for _, in := range cases {
		got := combineSameTableAlters(in)
		assert.Equal(t, in, got, "malformed ALTER must pass through and break the run")
	}
}

// TestCombineSameTableAlters_DropTableInteraction: a `DROP TABLE` is
// not an `ALTER TABLE` and breaks the run. (Realistic case: the diff
// pipeline never mixes Stmts and DropStmts buckets, but combineSameTableAlters
// must stay correct if a future caller hands it a mixed slice.)
func TestCombineSameTableAlters_DropTablePassThrough(t *testing.T) {
	in := []string{
		"ALTER TABLE t ADD COLUMN a INT;",
		"DROP TABLE u;",
		"ALTER TABLE t ADD COLUMN b INT;",
	}
	got := combineSameTableAlters(in)
	assert.Equal(t, in, got)
}
