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
you handwrite a `CREATE TABLE` from scratch.

**Migrating an existing schema into myschema.** Run `myschema dump`
against the live database first and use that output as your starting
point — every implicit FK covering index will be materialised in the
output, so the first `apply` is a no-op.
