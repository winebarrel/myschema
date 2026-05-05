# Getting started

This walkthrough takes a fresh MySQL database from empty to a
managed schema in about ten minutes. By the end you'll have:

- myschema installed and pointed at a database,
- a `desired.sql` file you check into version control,
- a `plan → apply → re-plan` round-trip that converges to "no
  changes", and
- enough orientation to dive into the deeper docs.

## 1. Install

```sh
go install github.com/winebarrel/myschema/cmd/myschema@latest
```

Or build from source:

```sh
git clone https://github.com/winebarrel/myschema
cd myschema
make build      # produces ./myschema
```

Requirements: **MySQL 8.0+**, Go 1.26+ to build. See `README.md`
for the full list.

## 2. Point myschema at a database

myschema operates on **exactly one database per invocation**. The
target database is part of the DSN — myschema doesn't take a
separate `--database` flag.

```sh
export MYSCHEMA_DSN='root@tcp(127.0.0.1:3306)/app'
```

The DSN format is whatever
[`go-sql-driver/mysql`](https://github.com/go-sql-driver/mysql#dsn-data-source-name)
accepts. Common variants:

```sh
# password inline
'user:secret@tcp(db.example.com:3306)/app'

# password via env (overrides whatever the DSN carries)
export MYSCHEMA_PASSWORD='secret'

# Unix socket
'root@unix(/tmp/mysql.sock)/app'
```

If the database doesn't exist yet, create it first — myschema
manages tables and views inside an existing database, not the
database itself:

```sh
mysql -uroot -e 'CREATE DATABASE app'
```

Sanity-check the connection:

```sh
myschema dump
# -- Dump of database app (0 table(s), 0 view(s))
```

## 3. Capture the current schema

`dump` serializes the live schema as SQL. Use it to bootstrap a
desired-side file even if the database already has tables:

```sh
myschema dump > desired.sql
```

The output is plain `CREATE TABLE` / `CREATE VIEW` statements.
Drop the header comment line if you prefer (it's just a summary;
not parsed back in).

## 4. Edit `desired.sql`

This is where the declarative model shines: you describe the
schema you want, not the steps to get there.

```sql
-- desired.sql
CREATE TABLE users (
    id BIGINT NOT NULL AUTO_INCREMENT,
    email VARCHAR(255) NOT NULL,
    name VARCHAR(64),
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (id),
    UNIQUE KEY users_email_key (email)
);

CREATE TABLE posts (
    id BIGINT NOT NULL AUTO_INCREMENT,
    user_id BIGINT NOT NULL,
    title VARCHAR(255) NOT NULL,
    body TEXT,
    PRIMARY KEY (id),
    KEY idx_posts_user (user_id),
    CONSTRAINT fk_posts_user FOREIGN KEY (user_id) REFERENCES users (id)
);
```

A few rules worth knowing up front (the rest are in
[`CAVEATS.md`](CAVEATS.md)):

- Every `FOREIGN KEY` needs an explicit covering index in desired
  SQL (see `idx_posts_user` above). myschema does **not** auto-skip
  the implicit index MySQL creates for un-indexed FKs.
- Use bare `INT` / `BIGINT`, **not** `INT(11)` / `BIGINT(20)` —
  display widths drift on every plan.
- Cross-database FK references are passed through, not managed.

## 5. Plan and apply

```sh
myschema plan desired.sql
```

Output is the DDL myschema would run, with `-- skipped:` comments
for anything blocked by the drop policy:

```sql
-- Plan: 0 → 2 table(s), 0 view(s)
CREATE TABLE users (...);
CREATE TABLE posts (...);
ALTER TABLE posts ADD CONSTRAINT fk_posts_user FOREIGN KEY (user_id) REFERENCES users (id);
```

Run it:

```sh
myschema apply desired.sql
```

A second `plan` should report no changes:

```sh
myschema plan desired.sql
# -- No changes.
```

That round-trip — `dump → edit → plan → apply → re-plan empty` —
is the core workflow.

## 6. Iterate: drops are opt-in

myschema refuses destructive operations by default. Removing the
`name` column from `users` and re-running plan shows what would
happen and what it skipped:

```sh
myschema plan desired.sql
# ALTER TABLE users DROP COLUMN name;        ← suppressed
# -- skipped: ALTER TABLE users DROP COLUMN name;
```

To allow specific drop categories:

```sh
myschema apply --allow-drop=column desired.sql
# or all destructive ops:
myschema apply --allow-drop=all desired.sql
```

The full set of categories is `table`, `view`, `column`,
`constraint`, `foreign_key`, `index`, `partition`, plus the `all`
wildcard.

## 7. Common flags

| Flag | What it does |
|------|---|
| `--include 'user*'`, `--exclude 'tmp_*'` | filter table names |
| `--allow-drop=…` | allow specific destructive op categories |
| `--alter-algorithm=INPLACE`, `--alter-lock=NONE` | inject MySQL online-DDL hints |
| `--bulk-alter` | fold consecutive same-table ALTERs into one multi-spec ALTER |
| `--pre-sql 'SET FOREIGN_KEY_CHECKS=0;'` | run session-level SQL before the diff |
| `--split=<dir>` (dump only) | one SQL file per table/view |

Every flag has a matching `MYSCHEMA_*` env var — useful in CI
where the DSN already lives in env. Run `myschema <cmd> --help`
for the full list.

## 8. Directives in desired SQL

myschema reads a few magic comments in the desired-side SQL. The
common ones:

- **`-- myschema:renamed-from <old>`** — rename a table, column,
  or index without losing the data.

  ```sql
  -- myschema:renamed-from old_users
  CREATE TABLE users (
      id BIGINT NOT NULL,
      -- myschema:renamed-from username
      email VARCHAR(255),
      PRIMARY KEY (id)
  );
  ```

- **`-- myschema:convert-charset`** — opt into the rebuild form of
  charset change (`ALTER TABLE … CONVERT TO CHARACTER SET …`)
  instead of the default two-stage flow.
- **`-- myschema:execute <check-sql>`** — escape hatch for objects
  myschema doesn't model (triggers, routines, events). The check
  SQL decides whether the guarded statement runs.

`AGENTS.md` lists every directive and `CAVEATS.md` documents the
sharp edges.

## What next

- [`AGENTS.md`](AGENTS.md) — feature surface, parser quirks,
  diff invariants. Read this before sending a PR.
- [`CAVEATS.md`](CAVEATS.md) — operational rules, sharp edges,
  things myschema deliberately doesn't manage.
- [`PARTITIONING.md`](PARTITIONING.md) — partition-diff scope and
  limits, including the shapes that error at plan time.
- The `make schema` target loads three real-world sample
  databases (Chinook, employees, sakila) into separate databases
  on your local MySQL, useful for ad-hoc `dump` testing.
