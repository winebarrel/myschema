# Caveats

Operational rules and known sharp edges when writing desired SQL for
myschema. None of these are bugs — they are deliberate design choices
that prefer "be explicit, fail loudly" over implicit catalog-side
inference.

## Foreign keys must declare a covering index

**Rule.** Every `FOREIGN KEY` in your desired SQL must be paired with
an explicit index whose left-most column list matches the FK's column
list. The index can be a `PRIMARY KEY`, a `UNIQUE` constraint, or a
plain secondary `KEY` / `INDEX` — anything that MySQL would treat as
a valid covering index for the FK.

```sql
-- Good: covering index declared explicitly.
CREATE TABLE order_items (
    id      BIGINT NOT NULL AUTO_INCREMENT,
    order_id BIGINT NOT NULL,
    product_id BIGINT NOT NULL,
    PRIMARY KEY (id),
    KEY idx_order_id (order_id),
    KEY idx_product_id (product_id),
    CONSTRAINT fk_order  FOREIGN KEY (order_id)   REFERENCES orders   (id),
    CONSTRAINT fk_product FOREIGN KEY (product_id) REFERENCES products (id)
);

-- Bad: FK without a matching KEY.
CREATE TABLE order_items (
    id      BIGINT NOT NULL AUTO_INCREMENT,
    order_id BIGINT NOT NULL,
    PRIMARY KEY (id),
    CONSTRAINT fk_order FOREIGN KEY (order_id) REFERENCES orders (id)
);
```

**Why myschema requires this.** When you create a foreign key on
columns that have no covering index, MySQL silently creates one for
you and names it after the FK. The `information_schema.STATISTICS`
view does **not** flag that index as implicit — it looks identical to
an index you wrote yourself. So when myschema diffs the catalog
(which has the implicit index) against your desired SQL (which only
has the FK), it sees an extra index and emits `DROP INDEX`. The drop
is suppressed unless `--allow-drop=index` is set, but if you do allow
it, `apply` then fails with `Error 1553` because the FK still needs
an index.

myschema deliberately does not try to detect and skip implicit FK
covering indexes. The judgement call: declarative tools work best
when "what you wrote" is what gets applied, and an implicit-index
heuristic would silently rewrite the user's intent. Requiring an
explicit covering index (PRIMARY KEY / UNIQUE / KEY) keeps the
desired SQL self-documenting and matches what `dump` already emits.

**Round-trip via `dump` is unaffected.** `myschema dump` always
materialises the covering index, so `dump → apply` (or `dump →
review → apply`) needs no manual fix-up. The rule only matters when
you handwrite the FK in desired SQL — whether inline inside a
`CREATE TABLE` or as a standalone `ALTER TABLE … ADD CONSTRAINT …
FOREIGN KEY` — without also writing the matching covering index.

**Migrating an existing schema into myschema.** Run `myschema dump`
against the live database first and use that output as your starting
point — every implicit FK covering index will be materialised in the
output, so the first `apply` is a no-op.

## Integer display widths drift; type-name casing doesn't

**Display widths.** Writing `INT(11)`, `BIGINT(20)`, `TINYINT(4)`,
etc. in desired SQL surfaces as drift on **every** plan and `apply`
never converges. Use the bare integer type (`INT`, `BIGINT`, …)
instead.

```sql
-- desired
CREATE TABLE t (id INT(11) NOT NULL);
-- catalog (after apply)
CREATE TABLE t (id int NOT NULL);
```

The next plan still shows `MODIFY COLUMN id int(11) NOT NULL`
because vitess keeps whatever the user wrote (`int(11)`) on the
desired side, while MySQL 8.0+ strips the display width from
`information_schema.COLUMNS.COLUMN_TYPE` (so the catalog returns
`int`). myschema does not normalise either side — comparing the
two literally produces a permanent diff.

**Why myschema doesn't auto-strip the width.** Two MySQL-8.0
edges make a "just strip `(N)`" rewrite unsafe at the parser
level: `TINYINT(1)` is the canonical storage type for `BOOLEAN` /
`BOOL` and the catalog *does* keep the `(1)`, and `ZEROFILL`
columns keep the width too (see below). Until myschema knows how
to differentiate those cases reliably, the rule is "write what
the catalog will hand back".

**`ZEROFILL` is the exception.** MySQL keeps the width in
`COLUMN_TYPE` for ZEROFILL columns (e.g. `int(5) unsigned
zerofill`), so writing the column the same way on the desired
side is round-trip safe — as long as the implicit `UNSIGNED` is
also spelled out (`ZEROFILL` implies `UNSIGNED`, but the catalog
emits both keywords explicitly):

```sql
-- round-trips fine
CREATE TABLE t (id INT(5) UNSIGNED ZEROFILL NOT NULL);
```

**Type-name casing.** `BIGINT` vs `bigint`, `VARCHAR` vs
`varchar`, etc. don't trigger drift — both sides are lower-cased
before comparison. Write whichever you find readable.

## Changing `DEFAULT CHARSET` on a table with string columns

**Behaviour.** `ALTER TABLE … DEFAULT CHARSET=…` only updates the
table-level default; MySQL freezes each pre-existing string column's
charset on the column row in `information_schema.COLUMNS` (the
column rows do *not* change automatically when the table default
changes). myschema's diff reflects this faithfully:

```sql
-- desired
CREATE TABLE t (name VARCHAR(64)) DEFAULT CHARSET=latin1;
-- catalog (built earlier with DEFAULT CHARSET=utf8mb4)
```

The first `myschema apply` emits one statement:

```sql
ALTER TABLE t DEFAULT CHARSET=latin1;
```

After that, the catalog still reports `name.CHARACTER_SET_NAME =
utf8mb4`, so the next `plan` shows a follow-up:

```sql
ALTER TABLE t MODIFY COLUMN name varchar(64);
```

(no explicit charset → the column inherits the new table default,
i.e. `latin1`). After the second apply the catalog and desired are
fully aligned and `plan` reports no further changes.

**Why this isn't auto-collapsed into a single apply.** Folding both
ALTERs into one plan would require myschema to predict the post-apply
catalog state instead of comparing against the actual one — the
project's declarative model is "compare current to desired and emit
the DDL". Two-stage convergence is the honest expression of that:
each `plan` describes what a *single* apply will do.

**`MYSCHEMA_VERIFY_NO_DRIFT` / `verify_no_drift: true` test fixtures.**
These fail on the first apply when a string-column table changes
charset, which is the same documented two-stage behaviour. Either run
apply a second time or, in fixtures, set `verify_no_drift: false` and
note the expected follow-up plan in a comment.

**Workarounds for "make it converge in one apply"**:

- Add `-- myschema:convert-charset` on the line above the
  `CREATE TABLE`. myschema then emits `ALTER TABLE … CONVERT TO
  CHARACTER SET <new> [COLLATE <new>]` (using the desired-side
  `DEFAULT CHARSET` / `COLLATE` clauses), which rewrites stored
  bytes and per-column charset metadata in a single statement.
  Heavyweight (full table rebuild), and column-level explicit
  charsets get clobbered by `CONVERT TO`, so columns that need
  to stay on a different charset still need their explicit
  `CHARACTER SET …` in desired SQL — myschema's column-level
  diff will emit the follow-up `MODIFY COLUMN` after the
  `CONVERT TO`.
- Spell out the per-column `CHARACTER SET …` in desired SQL alongside
  the `DEFAULT CHARSET` change. myschema's column-level diff then
  emits the `MODIFY COLUMN` in the same plan.
- Run `myschema apply` twice. The second is a no-op once the column
  charsets have inherited the new default.

## Partitioning: scoped to RANGE / LIST (incl. COLUMNS) suffix add + subset drop in v1

**Behaviour.** myschema reads partitioned tables (RANGE / LIST /
HASH / KEY / RANGE COLUMNS / LIST COLUMNS) round-trip — `dump`
emits the `PARTITION BY …` clause, `plan` reports no diff
against an unchanged catalog, and `apply` doesn't touch the
partition layout. Both sides normalise through the same vitess
pipeline so the comparison is bytewise.

**Diffs that *are* generated.** RANGE and LIST partitions —
including the column-list variants `RANGE COLUMNS(…)` and
`LIST COLUMNS(…)` — support the two most common operational
patterns:

- *Suffix add* (catalog is a strict prefix of desired) →
  `ALTER TABLE … ADD PARTITION (PARTITION p VALUES …)`.
  Typical use: roll the next month's / year's partition out
  ahead of writes. **Caveat:** if the live table already ends
  in a catch-all (RANGE `VALUES LESS THAN MAXVALUE` or LIST
  `VALUES IN (DEFAULT)`), inserting a new "real" partition in
  front of that catch-all is a mid-list change, not a suffix
  add — the new partition would land before the existing tail
  and the diff fails with the REORGANIZE error. Drop the
  catch-all first (or run REORGANIZE PARTITION by hand), then
  add the new partition.
- *Subset drop* — desired's partition list is `current`'s
  partition list with one or more entries removed (same order,
  same values for the entries that remain). Generates
  `ALTER TABLE … DROP PARTITION p1, p2`. Head, middle, and
  tail drops are all supported as long as the surviving order
  is preserved — the typical retention workflow ("trim the
  oldest partition") falls here. Gated by
  `--allow-drop=partition`; without that flag the DROP lands
  in the disallowed bucket as a `-- skipped:` line so the user
  sees what would have been removed.
- *HASH / KEY (incl. LINEAR) count change* — when both sides
  share the same partition strategy (Type / IsLinear /
  KeyAlgorithm / ColList / Expr), only `PARTITIONS n` differs.
  Generates the count-based grammar:
  - growing → `ALTER TABLE … ADD PARTITION PARTITIONS n`.
  - shrinking → `ALTER TABLE … COALESCE PARTITION n`. Merges
    the trailing partitions into the survivors. Gated on
    `--allow-drop=partition` not because rows are lost (they
    aren't — they're redistributed) but because the slot
    structure itself changes irreversibly: you can't
    un-COALESCE without another ALTER that rewrites data
    again, so it lines up with the same "destructive / heavy"
    treatment RANGE/LIST DROP gets. Without the flag the
    COALESCE lands on the disallowed bucket.

  Both directions are **row-preserving but data-moving**:
  changing the partition count moves rows between partitions
  on disk. The cost depends on which sub-strategy the table
  uses. Regular `HASH` / `KEY` use the partition-function
  modulus, so almost every row's target partition shifts and
  MySQL effectively rewrites the table — expect I/O
  proportional to table size on a large table. `LINEAR HASH`
  / `LINEAR KEY` use the linear-powers-of-two algorithm
  documented in the MySQL manual as making "adding, dropping,
  merging, and splitting of partitions … much faster" because
  only the partitions adjacent to the change need to be
  touched. If your table is large enough that the rewrite
  cost is the question driving the schema design, the
  regular vs. linear choice matters more than how
  myschema phrases the diff.

- *Per-partition definition change* — when the catalog and
  desired partition name lists line up position-by-position
  (every partition stays in the same slot, every name matches
  case-insensitively), any per-partition definition difference
  is generated as one or more `ALTER TABLE … REORGANIZE
  PARTITION p_i, p_{i+1}, … INTO (PARTITION p_i …,
  PARTITION p_{i+1} …, …)` statements. The most common shape
  is a `VALUES LESS THAN` / `VALUES IN` boundary tweak (e.g.
  `p2020 LESS THAN (2021)` → `p2020 LESS THAN (2025)`), but
  COMMENT / MAX_ROWS / TABLESPACE and other per-partition
  options that round-trip through vitess's PartitionDefinition
  formatter are picked up here too. Two semantic no-ops are
  *intentionally suppressed* even though they would otherwise
  surface as byte-different formatted definitions: case-only
  partition-name diffs (`pAB → PAB` — MySQL identifiers are
  case-insensitive) and LIST / LIST COLUMNS `VALUES IN (…)`
  permutations (the value list is a set semantically, so
  reordering literals doesn't change which rows land in which
  partition). Both are folded by `partitionDefEqual` and emit
  no DDL. How myschema slices the REORGANIZE statements
  depends on the partition strategy because the safety
  constraints differ:

    - **RANGE / RANGE COLUMNS**: one REORGANIZE per run of
      consecutive changed slots. RANGE values are continuous
      and bound by per-slot boundaries, so a value can only
      move to an adjacent slot when its boundary shifts —
      values can't cross over an unchanged slot in between.
      A "p1 + p3 boundary edit" 5-partition RANGE table
      emits two REORGANIZE statements rather than one giant
      `REORGANIZE p1, p2, p3, p4 …` that drags the unchanged
      p2 into the rewrite. Each run additionally extends by
      one slot when that run's last changed slot's `VALUES
      LESS THAN` actually moved AND that slot isn't the
      final partition — pulling `p_{last+1}` in re-establishes
      the boundary alignment with the unchanged tail
      (otherwise MySQL rejects with "VALUES less than value
      must be strictly increasing"). Per-partition option-only
      diffs leave the value range untouched, so the cascade
      doesn't fire — a metadata-only edit stays a one-slot
      REORGANIZE.

    - **LIST / LIST COLUMNS**: one single-span REORGANIZE
      covering [first..last]. LIST values are a discrete set
      per slot, and a value can move between any two
      partitions regardless of position. A "p0 gains 4, p3
      loses 4" cross-flow needs both ends of the move inside
      a single REORGANIZE — splitting into per-run
      REORGANIZEs would break MySQL's value-uniqueness rule
      (Error 1495 "Multiple definition of same constant in
      list partitioning") on the first statement while the
      second partition still owned the value. Matched
      partitions inside the span get re-stated unchanged
      (MySQL no-ops them). myschema doesn't try to detect
      whether a particular LIST diff is cross-flow-free and
      could safely be split — the tracking overhead isn't
      worth it for the common case.

  In both cases MySQL redistributes existing rows into the
  new boundaries (row-preserving). No `--allow-drop` gating
  because every dropped name is reused on the add side.
  **Operationally** REORGANIZE PARTITION is data-moving —
  every row in the named partitions is read, redistributed
  according to the new boundaries, and rewritten in place.
  Cost scales with the row count of the slots in the run (and
  the cascaded `last+1` slot when that fires), not with how
  much the boundary moved — even a one-byte VALUES tweak on a
  multi-million-row partition rewrites the whole partition.
  Same caveat as the HASH/KEY count-change section above: the
  diff is shaped to keep the rewrite proportional to the diff
  (per-run REORGANIZE, minimal cascade), but on large
  partitions the resulting ALTER is still a heavy operation —
  size your maintenance windows accordingly, or set
  `--alter-algorithm=COPY --alter-lock=SHARED` (or equivalent)
  to make MySQL's locking choice explicit.

**Diffs that still error (manage by hand).**

- *Split / merge / reorder* — any other "drops AND adds both
  non-empty" shape where the name lists don't line up
  position-by-position. Covers split / merge shapes whose
  add and drop names are disjoint (e.g. `[p0<10,p1<20,p2<30]
  → [p0<10,q1<40]`, where `q1` semantically inherits the
  rows from `p1` and `p2`), interior inserts in front of a
  catch-all, partition reorders, and the "retention
  roll-forward" pair (`[p2020,p2021] → [p2021,p2022]` —
  drops `p2020`, adds `p2022`, names disjoint). Inferring
  the right `REORGANIZE PARTITION old INTO (…)` boundaries
  from a name-only diff isn't safe (a DROP+ADD pair would
  silently lose data when the user really meant a split or
  merge), so the diff layer errors with `split / merge /
  reorder`. Workaround: run the appropriate ALTER by hand —
  `REORGANIZE PARTITION old1, old2 INTO (…)` with the
  boundaries you want, or `DROP PARTITION` + `ADD PARTITION`
  if discarding data is intended — then re-run plan.
- *Strategy / expression change* (e.g. RANGE → HASH, or a
  different `PARTITION BY` expression) — needs `REMOVE
  PARTITIONING` followed by a new `PARTITION BY`. Future PR.
  Fails with `partition strategy / expression differs`.
- *Adding or removing partitioning entirely* — both directions
  are future work, with separate error messages so the
  remediation hint matches the operation:
  - desired adds `PARTITION BY` to an unpartitioned catalog
    table → `first-time ALTER TABLE … PARTITION BY … is not
    yet generated by myschema`.
  - desired drops the `PARTITION BY` clause from an
    already-partitioned catalog table → `ALTER TABLE …
    REMOVE PARTITIONING is not yet generated by myschema`.
- *PRIMARY KEY / UNIQUE INDEX missing partition column(s)* —
  MySQL requires every unique key on a partitioned table to
  include all columns in the partitioning function. If
  desired's PRIMARY KEY or UNIQUE INDEX omits a column the
  desired-side `PARTITION BY` clause references, the diff
  fails at plan time with `… is missing partition column(s)
  [...]` instead of emitting an `ADD PRIMARY KEY` / `ADD
  UNIQUE INDEX` MySQL would reject. The check runs on both
  the diff path (already-existing tables) and the create-
  table path (brand-new partitioned tables) — apply doesn't
  get a chance to half-create.

  This guard also covers MySQL Error 3855 ("Column … has a
  partitioning function dependency and cannot be dropped or
  renamed") for the cases that arise from a *valid* desired
  CREATE TABLE: dropping a partition-key column requires the
  user to also drop the column from the unique key (else the
  CREATE TABLE wouldn't parse on real MySQL), and renaming
  one requires the desired-side `PARTITION BY` to use the
  new name (which the diff then surfaces as
  "partition strategy / expression differs"). Workarounds:
  include the partition columns in the unique key, or drop
  partitioning first (REMOVE PARTITIONING by hand) and let
  the next plan reconverge.

  **`--allow-drop` does not bypass this.** Even if you omit
  `--allow-drop=column` while removing a partition column
  from desired (so the DROP COLUMN itself lands in the
  disallowed bucket), the resulting desired-side PRIMARY KEY
  no longer covers the partition expression and the diff
  fails here before any of the surrounding ALTERs are
  emitted. That's intentional: a partial apply (PK shrunk,
  partition column still present in MySQL but uncovered)
  would leave the table in a state MySQL itself wouldn't
  accept on the next ALTER. Workaround: include the
  partition columns in the unique key, or drop partitioning
  first (REMOVE PARTITIONING by hand) and let the next plan
  reconverge.
- `SUBPARTITION BY …` is out of scope for v1. A desired-side
  `CREATE TABLE` that declares SUBPARTITION fails at parse
  time; a catalog-side table with SUBPARTITION is rejected
  when the partition clause is read back, so myschema won't
  try to manage it. Drop the SUBPARTITION (manually) before
  bringing the table under myschema's management.

**Workaround for the "two systems of record" problem.** When
the diff errors out — split / merge / reorder REORGANIZE
shapes, scheme / expression changes, adding or removing
partitioning entirely, SUBPARTITION — run the appropriate
ALTER by hand:

- *split / merge / reorder* — `ALTER TABLE … REORGANIZE
  PARTITION old1, old2 INTO (PARTITION newA VALUES …,
  PARTITION newB VALUES …)` with the boundaries you want
  (myschema can't infer split points safely from a name-only
  diff). For pure retention-discard the explicit
  `DROP PARTITION` + `ADD PARTITION` pair is the right call.
- *scheme / expression change* — `ALTER TABLE … REMOVE
  PARTITIONING` followed by a fresh `ALTER TABLE …
  PARTITION BY …`.
- *first-time partitioning* — `ALTER TABLE … PARTITION BY …`
  by hand once.
- *removing partitioning* — `ALTER TABLE … REMOVE
  PARTITIONING` by hand once.

After running the manual ALTER, update the desired SQL's
`PARTITION BY` clause (or remove it) to match. The next
`plan` will report no diff. The supported shapes —
RANGE/LIST suffix add, order-preserving subset DROP,
HASH/KEY (incl. LINEAR) count grow / shrink, and per-
partition definition rewrite via REORGANIZE PARTITION
(VALUES boundary tweaks plus COMMENT / MAX_ROWS /
TABLESPACE / other per-partition options that round-trip
through vitess's PartitionDefinition formatter) — don't
need a workaround; myschema generates them automatically.

## View `DEFINER` and `SQL SECURITY` are out of scope

**Behaviour.** myschema does not read `DEFINER` or `SECURITY_TYPE`
from `information_schema.VIEWS`, doesn't carry them on
`model.View`, doesn't diff them, and doesn't emit either clause
in `CreateSQL` on apply. The forms of `DEFINER=…` and `SQL
SECURITY {DEFINER | INVOKER}` that vitess can parse are silently
stripped out by the parser; the forms vitess can't parse (see
below) cause the desired SQL to fail at parse time. Either way,
nothing about the catalog-side DEFINER / SECURITY is touched.

**Why.**

- *DEFINER*: the vitess parser does not accept the canonical
  catalog-side host quoting that real MySQL servers return. Both
  `DEFINER='root'@'%'` and `DEFINER=root@'%'` fail at parse time,
  and the forms vitess does accept (e.g. `DEFINER=root@host` with
  a bare hostname) don't round-trip against the catalog's
  `root@%`. So even if myschema emitted DEFINER on apply, the
  next plan would either fail to parse or drift on every run.
- *SQL SECURITY*: technically diffable, but every view ships with
  `SECURITY_TYPE = DEFINER` by default — emitting that clause on
  every view would noise up dump output without serving a real
  user need, and the asymmetry of "DEFINER off, SQL SECURITY on"
  is worse than just leaving both alone for v1.

**Workaround.** Manage DEFINER / SECURITY by hand outside
myschema (`ALTER DEFINER=… SQL SECURITY {DEFINER|INVOKER} VIEW
…`, or `CREATE DEFINER=… SQL SECURITY {…} VIEW …` after a manual
`DROP VIEW`). myschema's view diff stays focused on the SELECT
body itself.

## Unmodelled SQL in desired-side files is silently skipped

**Behaviour.** Two skip paths in the parser silently drop SQL that
myschema doesn't model:

- *Top-level statements* other than `CREATE TABLE`, `CREATE VIEW`,
  and `ALTER TABLE` (the latter is also the AST shape vitess uses
  for user-written `CREATE INDEX`, so `CREATE INDEX` is supported
  even though it's not listed here). So `CREATE TRIGGER`,
  `CREATE PROCEDURE`, `CREATE FUNCTION`, `CREATE EVENT`,
  `CREATE DATABASE`, `SET …`, `INSERT`, `UPDATE`, `DELETE`,
  `SELECT`, `DROP TABLE`, `RENAME TABLE`, `TRUNCATE`, `ALTER VIEW`,
  non-directive comments — all parse successfully and produce
  nothing in the diff. (Comments that look like `-- myschema:<name>`
  are the exception: they go through `ValidateDirectives` first
  and unknown / malformed shapes error out at parse time.)
- *Unhandled `ALTER TABLE` clauses* on a table that is *also*
  declared elsewhere in the desired SQL. `ADD CONSTRAINT`
  (FK / CHECK) and `ADD INDEX` (the same AST shape vitess uses for
  user-written `CREATE INDEX`) flow through `applyAlterTable`
  into the in-memory model; everything else (`ADD COLUMN`,
  `MODIFY COLUMN`, `DROP COLUMN`, `DROP INDEX`, `RENAME COLUMN`,
  partition ops, …) is silently ignored. Note: this skip only
  applies when the target table is already in the desired model
  — `ALTER TABLE` against a table that isn't declared anywhere in
  the desired SQL fails fast with `ALTER TABLE on unknown table
  …`, not silently.

**Why this isn't an error.** This is the one intentional exception
to the "be explicit, fail loudly" stance the file intro sets out —
flagged here so the contrast doesn't read as inconsistent. The
default behaviour is permissive so a raw `mysqldump` output (which
is full of `SET …` / `CREATE DATABASE …` / `INSERT` / comments)
can be fed straight to `myschema apply` without manual editing. A
loud rejection would break that workflow.

**Impact.** A user who writes `ALTER TABLE t ADD COLUMN c INT` in
their desired SQL expecting it to land will get nothing — the
column never enters the desired-side model. The skip is silent, so
the symptom is "no observable plan output for that column": the
next `plan` reports either `-- No changes` or only the diff for
the *other* changes in the file, and `dump` over the live database
shows the table without the column. The user has to read back the
desired SQL or notice the missing column in `dump` output to
realise the ALTER did nothing. The desired state is meant to live
in `CREATE TABLE` (and `CREATE VIEW`), and the pipeline that turns
desired into actual ALTERs lives in `diff/`, not in user-written
DDL.

**Workaround.** Express schema state via `CREATE TABLE` — the
target shape is what gets compared against the catalog, and
myschema generates the ALTERs needed to bring current → desired.
Code-side state changes (`INSERT`, `UPDATE` for seed data, etc.)
belong outside myschema, in the same migration tooling that runs
schema apply.

A `--strict` mode that rejects unmodelled SQL instead of skipping
it would be useful for production CI but is out of scope for v1.

## `-- myschema:execute` is the only escape hatch for unmodelled objects

**Workflow.** myschema doesn't model triggers, stored
procedures / functions, events, or grants — they're imperative,
version-tagged code rather than declarative state. To manage
them alongside myschema's tables and views without leaving the
desired-side SQL file, prefix the unmodelled DDL with a
`-- myschema:execute <check-sql>` directive:

```sql
CREATE TABLE t (
    id BIGINT NOT NULL,
    val INT,
    PRIMARY KEY (id)
);

-- myschema:execute SELECT 1 FROM information_schema.TRIGGERS WHERE TRIGGER_NAME='trg' AND TRIGGER_SCHEMA=DATABASE()
CREATE TRIGGER trg BEFORE INSERT ON t FOR EACH ROW SET NEW.val = 0;
```

**Idempotency.** At plan / apply time myschema runs the check SQL
against the live database. Zero rows back means "not applied yet,
run the guarded statement"; one or more rows means "already
applied, skip". The guarded statement is therefore safe to leave
in desired SQL across re-applies — the second `apply` is a no-op
(the trigger now exists, the check SELECT returns a row, the
guarded `CREATE TRIGGER` is skipped).

**Apply order.** Execute groups run **after** every other DDL
bucket — table renames, FK drops, table CREATE / ALTER, table
drops, FK adds, view CREATE OR REPLACE — so a guarded `CREATE
TRIGGER` can reference brand-new tables.

**Limits (v1).**

- Each directive guards exactly one statement — the next statement
  in the file as cut by vitess's `SplitStatementToPieces`. The
  payload may span multiple lines; the boundary is the next `;`,
  not the next blank line. Multi-statement payloads aren't
  supported (see the next bullet); write a separate directive per
  statement.
- The guarded payload must contain **no internal `;`**. Vitess's
  `SplitStatementToPieces` (which myschema runs over the desired
  file before extracting directives) cuts pieces at every `;`, so
  a `CREATE TRIGGER … BEGIN …; …; END;` body splits across
  multiple pieces and either fails to parse on the orphan inner
  pieces or hands MySQL a truncated payload at apply time. This
  is the same reason MySQL CLI requires `DELIMITER //` for
  multi-statement TRIGGER / PROCEDURE bodies — myschema doesn't
  recognise `DELIMITER`. Workarounds: use the single-statement
  TRIGGER form (`FOR EACH ROW SET …`) when possible, or run the
  multi-statement body by hand outside myschema.
- The check SQL is parsed by vitess at desired-SQL parse time and
  must be **exactly one read-only statement**: `SELECT`, `UNION`,
  or `WITH … SELECT`. Multi-statement check SQL (a `;` between
  statements), DDL, DML, `SHOW`, `EXPLAIN`, etc. are rejected up
  front — the check runs on every plan / apply, so anything that
  could mutate the database needs to fail at parse time rather
  than during execution. At runtime the parsed statement is sent
  verbatim to `db.Query` and myschema only inspects whether the
  result set has at least one row: write a SELECT that returns a
  row when the guarded statement is "already applied" and zero
  rows otherwise. Prefer shapes that bound the result set to one
  row (`LIMIT 1`, `SELECT EXISTS(…)`, `SELECT 1 FROM … WHERE …
  LIMIT 1`) — myschema only calls `rows.Next()` once, but
  `rows.Close()` still drains any remaining rows so the connection
  can be reused, which makes a check that scans a large table
  unexpectedly expensive on every plan / apply.
- The guarded statement is held as raw SQL — myschema doesn't
  parse it (vitess can't parse `CREATE TRIGGER` and friends), so
  a syntax error surfaces only at apply time when MySQL rejects
  the statement.
- `dump` does not emit execute groups: the catalog has no
  awareness of them, and re-emitting trigger / routine bodies
  would require a separate `SHOW CREATE` pipeline that's out of
  scope. Keep the source-of-truth desired SQL file under version
  control alongside the guarded objects.
