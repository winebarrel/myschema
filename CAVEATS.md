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
