# Partitioning support in myschema

This document is the single source of truth for what myschema does with
partitioned tables. CAVEATS.md, AGENTS.md, and TODO.md all defer here.
The implementation lives in `diff/partitions.go` (diff generation),
`parser/partition.go` (clause parse / normalise / re-format), and the
catalog-side extraction in `catalog/tables.go`. User-facing fixtures
that pin every shape called out below are under `testdata/{plan,apply}`
with the `partition_*` prefix.

## Round-trip overview

myschema reads partitioned tables (RANGE / LIST / HASH / KEY /
RANGE COLUMNS / LIST COLUMNS) round-trip — `dump` emits the
`PARTITION BY …` clause, `plan` reports no diff against an unchanged
catalog, and `apply` doesn't touch the partition layout. Both sides
normalise through the same vitess pipeline so the comparison is
bytewise.

## Generated diffs

### Suffix add — RANGE / LIST

Catalog is a strict prefix of desired →
`ALTER TABLE … ADD PARTITION (PARTITION p VALUES …)`.
Typical use: roll the next month's / year's partition out ahead of
writes.

**Caveat.** If the live table already ends in a catch-all (RANGE
`VALUES LESS THAN MAXVALUE` or LIST `VALUES IN (DEFAULT)`),
inserting a new "real" partition in front of that catch-all is a
mid-list change, not a suffix add — the new partition would land
before the existing tail and the diff fails with the REORGANIZE
error. Drop the catch-all first (or run REORGANIZE PARTITION by
hand), then add the new partition.

### Subset drop — RANGE / LIST

Desired's partition list is `current`'s list with one or more
entries removed (same order, same values for the entries that
remain). Generates `ALTER TABLE … DROP PARTITION p1, p2`.

Head, middle, and tail drops are all supported as long as the
surviving order is preserved — the typical retention workflow
("trim the oldest partition") falls here. Gated by
`--allow-drop=partition`; without that flag the DROP lands in the
disallowed bucket as a `-- skipped:` line so the user sees what
would have been removed.

### HASH / KEY count change (incl. LINEAR)

When both sides share the same partition strategy
(Type / IsLinear / KeyAlgorithm / ColList / Expr), only
`PARTITIONS n` differs.

  - growing → `ALTER TABLE … ADD PARTITION PARTITIONS n`.
  - shrinking → `ALTER TABLE … COALESCE PARTITION n`. Merges the
    trailing partitions into the survivors. Gated on
    `--allow-drop=partition` not because rows are lost (they
    aren't — they're redistributed) but because the slot structure
    itself changes irreversibly: you can't un-COALESCE without
    another ALTER that rewrites data again, so it lines up with
    the same "destructive / heavy" treatment RANGE/LIST DROP gets.
    Without the flag the COALESCE lands on the disallowed bucket.

Both directions are **row-preserving but data-moving**: changing
the partition count moves rows between partitions on disk. The
cost depends on which sub-strategy the table uses. Regular `HASH`
/ `KEY` use the partition-function modulus, so almost every row's
target partition shifts and MySQL effectively rewrites the table —
expect I/O proportional to table size on a large table.
`LINEAR HASH` / `LINEAR KEY` use the linear-powers-of-two
algorithm documented in the MySQL manual as making "adding,
dropping, merging, and splitting of partitions … much faster"
because only the partitions adjacent to the change need to be
touched. If your table is large enough that the rewrite cost is
the question driving the schema design, the regular vs. linear
choice matters more than how myschema phrases the diff.

### Per-partition definition rewrite via REORGANIZE PARTITION

When the catalog and desired partition name lists line up
position-by-position (every partition stays in the same slot,
every name matches **case-insensitively** — `pAB` ≡ `PAB`, and a
stand-alone case-only rename emits no DDL), any per-partition
definition difference is generated as one or more
`ALTER TABLE … REORGANIZE PARTITION p_i, p_{i+1}, … INTO
(PARTITION p_i …, PARTITION p_{i+1} …, …)` statements.

The most common shape is a `VALUES LESS THAN` / `VALUES IN`
boundary tweak (e.g. `p2020 LESS THAN (2021)` →
`p2020 LESS THAN (2025)`), but COMMENT / MAX_ROWS / TABLESPACE
and other per-partition options that round-trip through vitess's
PartitionDefinition formatter are picked up here too.

Two semantic no-ops are *intentionally suppressed* even though
they would otherwise look byte-different:

- case-only partition-name diffs (`pAB → PAB` — MySQL identifiers
  are case-insensitive);
- LIST / LIST COLUMNS `VALUES IN (…)` permutations (the value list
  is a set semantically, so reordering literals doesn't change
  which rows land in which partition).

Both are folded by `partitionDefEqual` and emit no DDL.

How the REORGANIZE statements are sliced depends on the partition
strategy because the safety constraints differ:

- **RANGE / RANGE COLUMNS** — one REORGANIZE per run of consecutive
  changed slots. RANGE values are continuous and bound by per-slot
  boundaries, so a value can only move to an adjacent slot when its
  boundary shifts — values can't cross over an unchanged slot in
  between. A "p1 + p3 boundary edit" 5-partition RANGE table emits
  two REORGANIZE statements rather than one giant
  `REORGANIZE p1, p2, p3, p4 …` that drags the unchanged p2 into
  the rewrite. Each run additionally extends by one slot when that
  run's last changed slot's `VALUES LESS THAN` actually moved AND
  that slot isn't the final partition (the `VALUES LESS THAN <x>`
  of slot i is also the implicit lower bound of slot i+1, so MySQL
  needs slot i+1 in the REORGANIZE — otherwise it rejects with
  "VALUES less than value must be strictly increasing"). Per-
  partition option-only edits don't trigger that cascade, so a
  metadata-only change stays a one-slot REORGANIZE.

- **LIST / LIST COLUMNS** — per-run when no value leaves any
  changed slot (every changed slot's NEW `VALUES IN (…)` is a
  *superset* of its OLD set), single span otherwise. The superset
  check catches cross-flow cases like "p0 gains 4, p3 loses 4"
  where splitting into per-run REORGANIZEs would break MySQL's
  value-uniqueness rule (Error 1495 "Multiple definition of same
  constant in list partitioning") on the first statement while the
  second partition still owned the value. When the check fails
  myschema falls back to a single span over [first..last] with the
  matched in-betweens re-stated unchanged (MySQL no-ops them).
  The pure-addition case ("p0 gains 9, p3 gains 10",
  `p1`/`p2` unchanged) takes the per-run path so a tiny endpoint
  edit doesn't drag a wide partition table through a full
  data-moving rewrite. The check uses `sqlparser.String` keying so
  it's tuple-safe for LIST COLUMNS for free.

In both cases MySQL redistributes existing rows into the new
boundaries — *row-preserving when every catalog value still has a
home in the new layout*. The narrow exception is a LIST diff that
removes a constant from `VALUES IN (…)` without re-assigning it to
another partition: see "LIST value discard silently drops rows"
under plan-time errors below.

**Operationally** REORGANIZE PARTITION is data-moving — every row
in the named partitions is read, redistributed according to the
new boundaries, and rewritten in place. Cost scales with the row
count of the slots in the run (and the cascaded `last+1` slot when
that fires), not with how much the boundary moved — even a
one-byte VALUES tweak on a multi-million-row partition rewrites
the whole partition. Same caveat as the HASH/KEY count-change
section: the diff is shaped to keep the rewrite proportional to
the diff (per-run REORGANIZE, minimal cascade), but on large
partitions the resulting ALTER is still a heavy operation — size
your maintenance windows accordingly, or set
`--alter-algorithm=COPY --alter-lock=SHARED` (or equivalent) to
make MySQL's locking choice explicit.

The `appendAlterHints` rewrite that injects
`--alter-algorithm` / `--alter-lock` clauses needs the leading-
position splice for ANY partition operation (REORGANIZE / ADD /
DROP / COALESCE / TRUNCATE / EXCHANGE PARTITION) — MySQL rejects
the trailing-comma form on partition ops with Error 1064. The
hint inserter detects partition ops by skipping
`ALTER TABLE <name>` (back-tick / qualified-name aware) and
matching the alter-spec keyword that follows, then splices the
hints between the table name and the keyword:
`ALTER TABLE t ALGORITHM=…, LOCK=…, REORGANIZE PARTITION …`.
Column / index / constraint operations keep the trailing-comma
format MySQL accepts there.

## Plan-time validation errors

These checks reject invalid desired schemas at plan time so the
operator gets actionable feedback before any ALTER is emitted —
otherwise the failure (or in the LIST-discard case, silent data
loss) only surfaces deeper in the pipeline, after some statements
may already have run.

All of them run from both code paths: the modified-table path
(`diffPartitions`) and the create-table path (`DiffTables`'s
brand-new-table branch).

### LIST `VALUES IN` constants overlapping across partitions

MySQL forbids the same constant in more than one LIST partition
(Error 1495 "Multiple definition of same constant in list
partitioning"). myschema catches this at plan time with
`desired LIST partition definitions assign value V to both
partition pX and partition pY` so the operator gets actionable
feedback before any ALTER is emitted. Tuple-safe for `LIST
COLUMNS` (the constant is the whole tuple).

**Workaround.** Remove the duplicate from one of the partitions
in the desired SQL.

### LIST value discard silently drops rows on REORGANIZE

If desired removes a `VALUES IN (…)` constant *without* re-
assigning it to another partition, MySQL silently DROPs any rows
for that value on REORGANIZE — no apply-time error, no warning,
just data gone (verified against MySQL 8.0). Treated as
destructive and gated behind `--allow-drop=partition` (the same
flag DROP PARTITION / COALESCE PARTITION already uses for
partition-level data destruction).

The error reads:

```
desired LIST partition layout discards value V (catalog partition pX); MySQL silently drops any matching rows on REORGANIZE — re-add the value to a desired partition, or pass `--allow-drop=partition` to acknowledge the data loss
```

With the flag, the discard goes through as the operator's explicit choice.

The check sits inside the matched-name-list (REORGANIZE) branch
because it only matters for the REORGANIZE path — DROP PARTITION
already gates on `--allow-drop=partition` and surfaces a
`-- skipped:` line for unauthorised drops, and strategy mismatches
(LIST → HASH / RANGE / KEY) are caught earlier by
`partitionHeaderEqual`.

### RANGE `VALUES LESS THAN` not strictly increasing

MySQL rejects schemas like `p0 LESS THAN (25), p1 LESS THAN (20)`
with "VALUES less than value must be strictly increasing", but
vitess's parser doesn't enforce the rule, so the diff layer would
otherwise emit a REORGANIZE that can never apply. myschema catches
the non-monotonic ordering at plan time.

Comparison strategy:

- Integer-literal boundaries are compared numerically.
- MAXVALUE is treated as +∞: allowed at most once and only as the
  final partition.
- Non-integer / non-literal boundaries (function calls, RANGE
  COLUMNS tuples) only surface here on consecutive bytewise
  duplicates — deeper ordering for those falls back to MySQL's own
  error at apply time, because the diff layer doesn't have a
  general expression evaluator.

**Workaround.** Fix the boundary order in the desired SQL.

### PRIMARY KEY / UNIQUE INDEX missing partition column(s)

MySQL requires every unique key on a partitioned table to include
all columns in the partitioning function. If desired's PRIMARY KEY
or UNIQUE INDEX omits a column the desired-side `PARTITION BY`
clause references, the diff fails at plan time with `… is missing
partition column(s) [...]` instead of emitting an
`ADD PRIMARY KEY` / `ADD UNIQUE INDEX` MySQL would reject.

This guard also covers MySQL Error 3855 ("Column … has a
partitioning function dependency and cannot be dropped or
renamed") for the cases that arise from a *valid* desired CREATE
TABLE: dropping a partition-key column requires the user to also
drop the column from the unique key (else the CREATE TABLE
wouldn't parse on real MySQL), and renaming one requires the
desired-side `PARTITION BY` to use the new name (which the diff
then surfaces as "partition strategy / expression differs").

**`--allow-drop` does not bypass this.** Even if you omit
`--allow-drop=column` while removing a partition column from
desired (so the DROP COLUMN itself lands in the disallowed
bucket), the resulting desired-side PRIMARY KEY no longer covers
the partition expression and the diff fails here before any of
the surrounding ALTERs are emitted. That's intentional: a partial
apply (PK shrunk, partition column still present in MySQL but
uncovered) would leave the table in a state MySQL itself wouldn't
accept on the next ALTER.

**Workarounds.** Include the partition columns in the unique key,
or drop partitioning first (REMOVE PARTITIONING by hand) and let
the next plan reconverge.

## Diffs that still error — manage by hand

### Split / merge / reorder

Any other "drops AND adds both non-empty" shape where the name
lists don't line up position-by-position. Covers:

- split / merge shapes whose add and drop names are disjoint
  (e.g. `[p0<10,p1<20,p2<30] → [p0<10,q1<40]`, where `q1`
  semantically inherits the rows from `p1` and `p2`),
- interior inserts in front of a catch-all,
- partition reorders,
- the "retention roll-forward" pair (`[p2020,p2021] →
  [p2021,p2022]` — drops `p2020`, adds `p2022`, names disjoint).

Inferring the right `REORGANIZE PARTITION old INTO (…)` boundaries
from a name-only diff isn't safe (a DROP+ADD pair would silently
lose data when the user really meant a split or merge), so the
diff layer errors with `split / merge / reorder`.

**Workaround.** Run the appropriate ALTER by hand —
`REORGANIZE PARTITION old1, old2 INTO (…)` with the boundaries you
want, or `DROP PARTITION` + `ADD PARTITION` if discarding data is
intended — then re-run plan.

### Strategy / expression change

E.g. RANGE → HASH, or a different `PARTITION BY` expression.
Needs `REMOVE PARTITIONING` followed by a new `PARTITION BY`.
Future PR. Fails with `partition strategy / expression differs`.

**Workaround.** `ALTER TABLE … REMOVE PARTITIONING` followed by a
fresh `ALTER TABLE … PARTITION BY …` by hand, then re-run plan.

### Adding or removing partitioning entirely

Both directions are future work, with separate error messages so
the remediation hint matches the operation:

- desired adds `PARTITION BY` to an unpartitioned catalog table →
  `first-time ALTER TABLE … PARTITION BY … is not yet generated by
  myschema`.
- desired drops the `PARTITION BY` clause from an
  already-partitioned catalog table → `ALTER TABLE … REMOVE
  PARTITIONING is not yet generated by myschema`.

**Workaround.** Run the matching ALTER by hand once, then re-run
plan.

### SUBPARTITION

`SUBPARTITION BY …` is out of scope for v1. A desired-side
`CREATE TABLE` that declares SUBPARTITION fails at parse time; a
catalog-side table with SUBPARTITION is rejected when the
partition clause is read back, so myschema won't try to manage it.

**Workaround.** Drop the SUBPARTITION (manually) before bringing
the table under myschema's management.

## Workaround for the "two systems of record" problem

When the diff errors out — split / merge / reorder REORGANIZE
shapes, scheme / expression changes, adding or removing
partitioning entirely, SUBPARTITION — run the appropriate ALTER by
hand and update the desired SQL's `PARTITION BY` clause (or remove
it) to match. The next `plan` will report no diff. Operations
covered automatically (suffix add, subset drop, HASH/KEY count
change, per-partition definition rewrite) don't need a workaround;
myschema generates them.
