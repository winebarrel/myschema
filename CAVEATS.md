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

- Spell out the per-column `CHARACTER SET …` in desired SQL alongside
  the `DEFAULT CHARSET` change. myschema's column-level diff then
  emits the `MODIFY COLUMN` in the same plan.
- Run `myschema apply` twice. The second is a no-op once the column
  charsets have inherited the new default.
- For full data-rewrite semantics (`ALTER TABLE … CONVERT TO CHARACTER
  SET …`, which also rewrites stored bytes), run that DDL by hand
  outside myschema. A future directive may wire it in.

## View `DEFINER` and `SQL SECURITY` are out of scope

**Behaviour.** myschema reads both `DEFINER` and `SECURITY_TYPE`
from `information_schema.VIEWS` and stores them on `model.View`,
but the diff ignores both and `CreateSQL` does not emit either
clause on apply. Setting `DEFINER=…` or `SQL SECURITY {DEFINER |
INVOKER}` in the desired SQL has no effect — myschema neither
fails on it nor acts on it.

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

