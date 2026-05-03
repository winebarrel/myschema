package parser_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/winebarrel/myschema/parser"
)

func TestParseSQLPartitionRoundTrip(t *testing.T) {
	// Each case feeds the same partition shape through ParseSQL and
	// then through ExtractPartitionFromShowCreate's wrapper trick.
	// Both pipelines normalise via NormalizePartitionOption, so the
	// strings stored on model.Table.Partition must be byte-equal —
	// that's the whole reason a partitioned `dump → plan` reports
	// no diff in v1.
	cases := map[string]struct {
		sql      string
		showLike string // simulates a `SHOW CREATE TABLE` partition tail (versioned-comment wrapped)
	}{
		"RANGE": {
			sql: `CREATE TABLE t (id INT, dt DATE) PARTITION BY RANGE (YEAR(dt)) (
				PARTITION p2020 VALUES LESS THAN (2021),
				PARTITION p2021 VALUES LESS THAN (2022),
				PARTITION pmax VALUES LESS THAN MAXVALUE
			);`,
			showLike: "CREATE TABLE `t` (...)\n/*!50100 PARTITION BY RANGE (YEAR(dt))\n(PARTITION p2020 VALUES LESS THAN (2021) ENGINE = InnoDB,\n PARTITION p2021 VALUES LESS THAN (2022) ENGINE = InnoDB,\n PARTITION pmax VALUES LESS THAN MAXVALUE ENGINE = InnoDB) */",
		},
		"LIST": {
			sql: `CREATE TABLE t (id INT, region INT) PARTITION BY LIST (region) (
				PARTITION pA VALUES IN (1, 2, 3),
				PARTITION pB VALUES IN (4, 5)
			);`,
			showLike: "CREATE TABLE `t` (...)\n/*!50100 PARTITION BY LIST (region)\n(PARTITION pA VALUES IN (1,2,3) ENGINE = InnoDB,\n PARTITION pB VALUES IN (4,5) ENGINE = InnoDB) */",
		},
		"HASH with PARTITIONS count": {
			sql:      `CREATE TABLE t (id INT) PARTITION BY HASH (id) PARTITIONS 4;`,
			showLike: "CREATE TABLE `t` (...)\n/*!50100 PARTITION BY HASH (id)\nPARTITIONS 4 */",
		},
		"KEY": {
			sql:      `CREATE TABLE t (id INT) PARTITION BY KEY (id) PARTITIONS 4;`,
			showLike: "CREATE TABLE `t` (...)\n/*!50100 PARTITION BY KEY (id) PARTITIONS 4 */",
		},
		"RANGE COLUMNS": {
			sql: `CREATE TABLE t (a INT, b INT) PARTITION BY RANGE COLUMNS(a, b) (
				PARTITION p0 VALUES LESS THAN (10, 100),
				PARTITION p1 VALUES LESS THAN (20, 200)
			);`,
			showLike: "CREATE TABLE `t` (...)\n/*!50100 PARTITION BY RANGE COLUMNS(a,b)\n(PARTITION p0 VALUES LESS THAN (10,100) ENGINE = InnoDB,\n PARTITION p1 VALUES LESS THAN (20,200) ENGINE = InnoDB) */",
		},
		"LIST COLUMNS with string literals": {
			// Pins two things at once:
			//   - LIST COLUMNS(...) is supported (vitess emits the
			//     same shape as RANGE COLUMNS, just Type=3).
			//   - The normaliser only lowercases function-name and
			//     column-reference identifiers; string literals
			//     (`'A'`, `'B'`) must round-trip case-preserved
			//     because flipping `'A'` to `'a'` would change the
			//     value MySQL stores.
			sql: `CREATE TABLE t (id INT, region_code CHAR(1)) PARTITION BY LIST COLUMNS(region_code) (
				PARTITION pAB VALUES IN ('A', 'B'),
				PARTITION pCD VALUES IN ('C', 'D')
			);`,
			showLike: "CREATE TABLE `t` (...)\n/*!50100 PARTITION BY LIST COLUMNS(region_code)\n(PARTITION pAB VALUES IN ('A','B') ENGINE = InnoDB,\n PARTITION pCD VALUES IN ('C','D') ENGINE = InnoDB) */",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			res, err := parser.ParseSQL(tc.sql, "shop")
			require.NoError(t, err)
			tbl, ok := res.Tables.GetOk("shop.t")
			require.True(t, ok)
			require.NotNil(t, tbl.Partition)
			fromParser := *tbl.Partition

			fromCatalog, err := parser.ExtractPartitionFromShowCreate(tc.showLike)
			require.NoError(t, err)
			require.NotEmpty(t, fromCatalog)

			assert.Equal(t, fromParser, fromCatalog,
				"parser and SHOW CREATE TABLE must normalise to the same string — that's what makes dump→plan return no diff")
		})
	}
}

func TestExtractPartitionFromShowCreateWithPriorVersionedBlock(t *testing.T) {
	// MySQL 8.0+ can emit other versioned-comment blocks before the
	// partition one (e.g. /*!80016 ENCRYPTION='N' */ /*!50100
	// PARTITION BY … */). The extractor must locate the *trailing*
	// block, not the first /*! it finds — otherwise body would start
	// with ENCRYPTION='N' and the function would silently return
	// ("", nil), dropping the partition metadata.
	clause, err := parser.ExtractPartitionFromShowCreate("CREATE TABLE `t` (\n  `id` int NOT NULL,\n  `dt` date NOT NULL,\n  PRIMARY KEY (`id`,`dt`)\n) ENGINE=InnoDB /*!80016 ENCRYPTION='N' */\n/*!50100 PARTITION BY RANGE (year(`dt`))\n(PARTITION p2020 VALUES LESS THAN (2021) ENGINE = InnoDB,\n PARTITION p2021 VALUES LESS THAN (2022) ENGINE = InnoDB) */")
	require.NoError(t, err)
	assert.Equal(t,
		"partition by range (year(dt))\n(partition p2020 values less than (2021),\n partition p2021 values less than (2022))",
		clause,
	)
}

func TestNormalizePartitionOptionPreservesLiteralsContainingEngineKeyword(t *testing.T) {
	// Regression: round 11 replaced a regex-based per-partition
	// `engine <name>` strip with an AST-level `def.Options.Engine =
	// nil` because the regex wasn't quote-aware — a LIST value like
	// `'foo engine innodb'` would otherwise have its substring
	// chewed away, breaking both ADD PARTITION output and
	// partitionDefEqual comparisons. Pin the expected
	// round-trip so the AST approach can't slip back.
	res, err := parser.ParseSQL(`CREATE TABLE t (
    id INT,
    label VARCHAR(64) NOT NULL,
    PRIMARY KEY (id, label)
) PARTITION BY LIST COLUMNS(label) (
    PARTITION pA VALUES IN ('foo engine innodb', 'bar')
);`, "shop")
	require.NoError(t, err)
	tbl, ok := res.Tables.GetOk("shop.t")
	require.True(t, ok)
	require.NotNil(t, tbl.Partition)
	// The literal must round-trip whole — `'foo engine innodb'`
	// stays one quoted string. Under the previous regex-based
	// strip the substring ` engine innodb` (followed by `,`) was
	// chewed, leaving `'foo '` and `'` adjacent to the next
	// value; this assertion fails on that path.
	assert.Contains(t, *tbl.Partition, "'foo engine innodb'")
}

func TestExtractPartitionFromShowCreateNotPartitioned(t *testing.T) {
	// SHOW CREATE TABLE for a non-partitioned table has no
	// `/*!50100 PARTITION BY …` comment block. The extractor returns
	// ("", nil) — the catalog loader uses that as "leave Partition
	// nil".
	clause, err := parser.ExtractPartitionFromShowCreate("CREATE TABLE `t` (\n  `id` int NOT NULL,\n  PRIMARY KEY (`id`)\n) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4")
	require.NoError(t, err)
	assert.Equal(t, "", clause)
}

func TestParseSQLPartitionAbsentLeavesNil(t *testing.T) {
	res, err := parser.ParseSQL(`CREATE TABLE t (id INT, PRIMARY KEY (id));`, "shop")
	require.NoError(t, err)
	tbl, ok := res.Tables.GetOk("shop.t")
	require.True(t, ok)
	assert.Nil(t, tbl.Partition)
}

func TestParseSQLRejectsSubpartitionInDesiredSQL(t *testing.T) {
	// SUBPARTITION BY is out of scope for v1 — fail at parse time
	// rather than silently accepting it and then erroring later in
	// the diff (or worse, drifting silently).
	_, err := parser.ParseSQL(`CREATE TABLE t (id INT, dt DATE, PRIMARY KEY (id, dt))
PARTITION BY RANGE (YEAR(dt))
SUBPARTITION BY HASH (id) SUBPARTITIONS 2 (
    PARTITION p2020 VALUES LESS THAN (2021),
    PARTITION p2021 VALUES LESS THAN (2022)
);`, "shop")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "SUBPARTITION")
	assert.Contains(t, err.Error(), "out of scope")
}

func TestExtractPartitionFromShowCreateRejectsSubpartition(t *testing.T) {
	// Catalog-side guard: a table that already has SUBPARTITION
	// in its SHOW CREATE TABLE output is rejected so the catalog
	// loader fails fast instead of producing a partition string
	// myschema doesn't actually support.
	_, err := parser.ExtractPartitionFromShowCreate("CREATE TABLE `t` (...)\n/*!50100 PARTITION BY RANGE (YEAR(dt))\nSUBPARTITION BY HASH (id) SUBPARTITIONS 2\n(PARTITION p2020 VALUES LESS THAN (2021)\n (SUBPARTITION s0 ENGINE = InnoDB,\n  SUBPARTITION s1 ENGINE = InnoDB)) */")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "SUBPARTITION")
	assert.Contains(t, err.Error(), "out of scope")
}
