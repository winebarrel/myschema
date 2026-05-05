# Caveats

Operational rules and known sharp edges when writing desired SQL for
myschema. None of these are bugs — they are deliberate design choices
that prefer "be explicit, fail loudly" over implicit catalog-side
inference.

## What myschema deliberately doesn't manage

Two object families are **out of scope**, with different reasons.
Neither is a TODO item — they are intentionally not on the
roadmap.

- **Triggers, stored procedures / functions, events.** Imperative,
  version-tagged code rather than declarative schema. The catalog
  representation is a single text body that's hard to diff
  meaningfully (a function with a one-line body change has the same
  shape as one with an algorithmic rewrite). Manage them out of band
  with hand-written DDL, or use the `-- myschema:execute` directive
  (see "`-- myschema:execute` is the only escape hatch for unmodelled
  objects" below) when you want myschema to run a guarded one-shot
  for you.
- **Sequences.** MySQL has no sequence object — `AUTO_INCREMENT` is
  the closest thing and is already first-class on `model.Column`.
  TiDB *does* ship a `CREATE SEQUENCE`, but myschema is targeted at
  MySQL specifically (vitess parser, go-sql-driver/mysql), so a TiDB
  profile would be a separate effort and isn't planned.

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

## Foreign keys to tables in another database are passed through, not managed

**Behaviour.** A foreign key whose target lives in a different
database than the one myschema is invoked against
(`REFERENCES other_db.parent (id)`) is accepted by the parser and
faithfully round-tripped, but myschema does **not** manage the
parent side. Specifically:

- The parser stores `model.ForeignKey.RefDB = "other_db"` (cross-DB
  qualifiers on the REFERENCES target are intentionally allowed, in
  contrast to the *table-name* qualifier on the owning table which
  is rejected at parse time).
- `(*ForeignKey).SQL()` preserves the `other_db.` prefix on emit
  whenever `RefDB != Database`, so a hand-written cross-DB FK
  doesn't silently re-target a same-named table in the current DB.
- `dump` emits the FK as a separate statement, matching what the
  user would write by hand:

  ```sql
  ALTER TABLE child ADD CONSTRAINT fk_parent FOREIGN KEY (parent_id) REFERENCES other_db.parent (id);
  ```
- Diff still detects changes on the *child* side: equality compares
  `(RefDB, RefTable)`, so renaming or re-pointing the FK fires the
  expected `DROP FOREIGN KEY + ADD CONSTRAINT`.

**What myschema does NOT do.**

- **No plan-time validation that the parent exists.** myschema
  reads `information_schema` for the invocation database only;
  `other_db.parent` is invisible to it. A typo in the parent
  table name surfaces as a MySQL error at apply time
  (`Error 1146 Table 'other_db.parent' doesn't exist`,
  `Error 1822 Failed to add the foreign key constraint`, etc.) —
  not as a clean `plan` error.
- **No topological ordering with the parent's lifecycle.** Same-DB
  cross-table FKs are sequenced naturally because all `CREATE TABLE`
  statements run before the `FKAddStmts` bucket. Cross-DB FKs are
  emitted in that same bucket, but myschema can't sequence them
  against the parent DB's schema changes — the operator has to
  apply changes to `other_db` *before* any plan that references
  it lands.
- **Limited drift detection on the parent side.** myschema reads
  the child's FK row only; whatever MySQL writes into
  `information_schema.KEY_COLUMN_USAGE` is what the diff sees.
  That covers FK-metadata-level changes (a parent table rename
  via `RENAME TABLE` or a parent column rename via `ALTER TABLE
  RENAME COLUMN` cascades into the child's FK row, so the diff
  fires `DROP FOREIGN KEY + ADD CONSTRAINT` to restore the
  desired reference) but **not** structural changes that don't
  touch the FK metadata: parent column type widening / narrowing,
  parent unique-key reshuffles, parent collation changes, or any
  hand-written `foreign_key_checks=0` surgery. Those silently
  diverge until something forces MySQL to revalidate the FK
  (a write that touches the constrained column, an integrity
  check tool, …), at which point the failure surfaces unrelated
  to any plan run.

**Why myschema doesn't manage cross-DB FKs.** The "one database
per invocation" contract (the DSN carries it; emitted DDL is
unqualified) is the load-bearing simplification behind the rest
of the tool — schema ordering, drop policy, dump output, all
assume a single managed namespace. Reaching into `other_db` would
require either a multi-DSN model or speculative cross-database
reads under the same connection, both of which expand the
operational surface (privileges, DSNs, error paths) far beyond
what the project's scope supports. So cross-DB FKs are
"transparently passed through" rather than managed.

**What to do instead.** Manage `other_db` with its own
myschema invocation (or by hand). Sequence applies so the
parent table exists with the required columns / unique key
*before* any plan that references it lands. Use `dump` to
verify the child-side FK survived the round trip.

## Bulk-alter does not combine FK operations

**Behaviour.** `--bulk-alter` (default off) folds *consecutive*
same-table single-spec `ALTER TABLE` statements into one multi-spec
ALTER. ADD / DROP FOREIGN KEY are intentionally left out of the
combinable set — even when they target the same table that just had
columns added — and surface as their own separate `ALTER TABLE …
ADD CONSTRAINT …` / `… DROP FOREIGN KEY …` statements.

**Why.** myschema's diff pipeline keeps FK ops in two dedicated
buckets (`FKDropStmts`, `FKAddStmts`) and orders them as
`FK drops → table changes → FK adds`. The order is load-bearing:

- An FK that points at a brand-new table only exists once that
  parent's `CREATE TABLE` has run, so all FK adds must run *after*
  every other table change in the plan.
- An FK pointing at a column being dropped or renamed must be
  dropped *before* the parent column changes, so all FK drops
  must run *before* every other table change.

If `--bulk-alter` mixed FK ops into the column-change bucket, the
combined `ALTER TABLE child ADD COLUMN …, ADD CONSTRAINT fk_parent
…` would run *during* the table-change phase, not *after* the FK-add
phase — silently breaking the cross-table ordering invariant. By the
time MySQL rejected it (or worse, accepted it in the wrong order
because the parent happened to already exist) the bug would only
surface when a plan actually had a brand-new parent table to
reference.

The two-statement output is the cost of keeping the ordering safe.
Same-table column / constraint / DEFAULT-CHARSET / COMMENT / DROP
INDEX changes still combine — only FK operations stay separate.

**What myschema also does NOT combine** (each acts as a run
separator under `--bulk-alter`):

- Partition operations (`REORGANIZE PARTITION`, `ADD PARTITION`,
  `DROP PARTITION`, `COALESCE PARTITION`, `TRUNCATE PARTITION`,
  `EXCHANGE PARTITION`). MySQL's grammar rejects the trailing-comma
  multi-spec form for these clauses (the comma after `INTO (…)` or
  the partition list parses as a new partition definition); the
  combiner detects the keyword via the same `partitionOpInsertPos`
  helper `--alter-algorithm` uses for the same reason.
- **Secondary-index ADDs.** `diff/tables.go` emits the new index as
  a standalone `CREATE INDEX … ON t (…);` — not an
  `ALTER TABLE … ADD KEY …` — for both brand-new and modified
  tables. Different statement shape; never folded. (Index *DROPs*
  emit as `ALTER TABLE … DROP INDEX …;` and *do* combine.) An apply
  with `--bulk-alter` against a desired SQL that adds three columns
  and one secondary index produces one combined ALTER plus a
  separate CREATE INDEX, not a single ALTER carrying both.
- **`RENAME COLUMN` / `RENAME INDEX`.** Within one ALTER, MySQL
  resolves spec-target identifiers (e.g. the column name in
  `MODIFY COLUMN <name>`) against the *original* table state, so a
  follow-on spec referring to the renamed object by its new name
  fails at apply time with `Error 1054 Unknown column …` (or
  similar for indexes). To stay safe across all rename + follow-up
  shapes the diff might emit, splitCombinableAlter rejects any
  spec starting with `RENAME COLUMN` or `RENAME INDEX` — the
  rename keeps its own ALTER even under `--bulk-alter`. (Whole-
  table `RENAME TO new_table` lives in the `RenameStmts` bucket
  upstream and never reaches the combiner.)
- `CREATE TABLE`, `DROP TABLE`, `RENAME TABLE`.

**`--bulk-alter` interacts with `--alter-algorithm` /
`--alter-lock`.** When the two flags are used together, every spec
in a combined ALTER shares one ALGORITHM= / LOCK= clause —
MySQL applies it to the whole statement, not per-spec. The
trailing-comma splice that `appendAlterHints` does still fires
(combine first, then hint), so the syntax is correct, but the
*operational* effect changes:

- A run of two specs that would each apply INSTANT separately
  (e.g. two `ADD COLUMN` adds) folded into one ALTER with
  `--alter-algorithm=INSTANT` is fine — both qualify.
- A run that mixes an INSTANT-eligible spec (ADD COLUMN) with one
  that requires INPLACE / COPY (e.g. DEFAULT CHARSET change,
  generated-column add, certain MODIFY COLUMNs) becomes one
  ALTER with the user-supplied ALGORITHM=INSTANT — MySQL
  rejects it at apply time because the most-restrictive spec in
  the bundle isn't INSTANT-compatible.
- Without `--bulk-alter` the same two specs are two separate
  ALTERs, and MySQL's per-statement default ALGORITHM picks the
  right level for each. Operators who pin ALGORITHM to a strict
  level (INSTANT especially) need to weigh this against the
  combine: enabling `--bulk-alter` can turn a previously
  online-DDL-clean migration into a COPY-or-fail.

Run `plan` first to see the combined statement, and check each
spec against MySQL's online-DDL matrix. Drop one of the flags if
the combined ALGORITHM/LOCK choice doesn't fit.

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

## `ENUM` / `SET` element-list changes are diffed as one opaque string

**Behaviour.** `model.Column.TypeName` holds the rendered type
(`enum('new','paid','shipped')`) as a single lowercase string, and
the column diff (`columnEqual` in `diff/tables.go`) compares it
byte-for-byte. Any element-list change — append, reorder, remove,
rename — surfaces as the same generic `MODIFY COLUMN`, with no hint
about which kind it is and no `--allow-drop` accounting.

```sql
CREATE TABLE orders (
  id     BIGINT NOT NULL,
  status ENUM('new','paid','shipped') NOT NULL,
  PRIMARY KEY (id)
);
```

The four element-list shapes — each just a different `status` column
line in your desired `CREATE TABLE` — are:

- **Append**: `status ENUM('new','paid','shipped','refunded') NOT NULL`
- **Reorder**: `status ENUM('paid','new','shipped') NOT NULL`
- **Remove**: `status ENUM('new','paid') NOT NULL`
- **Rename**: `status ENUM('new','settled','shipped') NOT NULL`

Every shape surfaces as the same kind of statement — a single
`MODIFY COLUMN` whose `enum(...)` body mirrors whatever the user
wrote — so plan output looks structurally identical even though
the rendered enum literal and the runtime impact (online-safe
append, integer-mapping rewrite, data truncation, …) differ
sharply per shape:

```sql
-- append
ALTER TABLE orders MODIFY COLUMN status enum('new','paid','shipped','refunded') NOT NULL;
-- reorder
ALTER TABLE orders MODIFY COLUMN status enum('paid','new','shipped') NOT NULL;
-- remove
ALTER TABLE orders MODIFY COLUMN status enum('new','paid') NOT NULL;
-- rename
ALTER TABLE orders MODIFY COLUMN status enum('new','settled','shipped') NOT NULL;
```

**Why each shape matters differently.** ENUM stores values as
1-based integers (`'new'=1, 'paid'=2, 'shipped'=3`) and SET as
bit positions (`urgent=1, vip=2, beta=4`). The four shapes have
very different runtime cost and safety:

- **Append** (add a value at the *end* of the list): online-safe in
  MySQL 8.0+ and qualifies for `ALGORITHM=INSTANT`. myschema does
  not detect this and emits a plain `MODIFY COLUMN`. To run it
  online today, pass `--alter-algorithm=INSTANT` (and let MySQL
  reject the apply if the change isn't actually instant-eligible).
- **Reorder**: silently rewrites the integer mapping. Existing
  rows are reinterpreted — a row with `status='new'` becomes
  `status='paid'` after `ENUM('paid','new',…)`. MySQL doesn't warn,
  myschema doesn't warn, and the plan looks identical to the
  append case.
- **Remove**: rows holding the removed value are truncated to the
  empty string (or rejected, depending on `sql_mode`). myschema
  does **not** require `--allow-drop=column` for this — the
  policy applies to whole-column drops, not enum-value drops —
  so the removal slips through with the same `MODIFY COLUMN`.
- **Rename in place** (e.g. position 2 `'paid'` → `'settled'`,
  positions 1 and 3 unchanged): same risk profile as reorder, not
  truncation. ENUM is stored as a 1-based integer index and SET as
  a bit position; renaming a label without shifting its position
  leaves the stored integers as-is and silently relabels them, so
  rows that previously displayed `'paid'` now display `'settled'`
  with no warning. (A "rename" that also moves the value to a
  different position collapses back into the reorder case above.)

**Workarounds.**

- For appends, pass `--alter-algorithm=INSTANT`. Caveat: this flag
  is plan-wide — myschema injects the `ALGORITHM=INSTANT` hint into
  **every** generated `ALTER TABLE` and `CREATE INDEX` (see
  `appendAlterHints`), with statement-specific syntax: `ALTER TABLE`
  takes a leading-comma clause (`…, ALGORITHM=INSTANT`) appended
  before the trailing `;` (or spliced before the keyword for
  partition operations whose grammar rejects the trailing-comma
  position), while `CREATE INDEX` takes a space-separated trailing
  clause (`… ALGORITHM=INSTANT`). Either way, any non-INSTANT-eligible
  operation in the same plan (e.g. an index add that needs `INPLACE`
  / `COPY`) will fail at apply time. Either run the ENUM-append apply
  on its own (split your desired SQL or use `--include` / `--exclude`
  to scope the plan to the one table) or accept that the rest of
  the plan also needs to be INSTANT-eligible.
- For reorders / removes / renames, treat the desired-side change
  as destructive. Inspect plan output, take a backup, and consider
  staging the change (add the new value first, migrate data, then
  remove the old value).
- If you need element-level fidelity in the diff, manage the column
  out of band (run the `ALTER TABLE` by hand) and keep the
  desired-side type aligned with what the catalog reports.

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

## Partitioning

Partitioning has its own scope rules, plan-time validation errors,
and operational caveats. See [PARTITIONING.md](PARTITIONING.md) for
the full picture — what diffs are generated automatically (RANGE /
LIST suffix add, subset drop, HASH/KEY count change, per-partition
definition rewrite via REORGANIZE), what plan-time errors fire
(LIST overlap / value discard / RANGE non-monotonic / unique-key
coverage gap), and what shapes still require a manual ALTER (split /
merge / reorder, strategy change, adding or removing partitioning
entirely, SUBPARTITION).

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
