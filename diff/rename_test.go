package diff_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/winebarrel/myschema/diff"
	"github.com/winebarrel/myschema/model"
	"github.com/winebarrel/orderedmap"
)

// helpers ------------------------------------------------------------------

func tbl(db, name string) *model.Table {
	return &model.Table{
		Database:    db,
		Name:        name,
		Columns:     orderedmap.New[string, *model.Column](),
		Constraints: orderedmap.New[string, *model.Constraint](),
		ForeignKeys: orderedmap.New[string, *model.ForeignKey](),
		Indexes:     orderedmap.New[string, *model.Index](),
	}
}

func col(name, typ string) *model.Column {
	return &model.Column{Name: name, TypeName: typ, NotNull: true}
}

func pkConstraint() *model.Constraint {
	return &model.Constraint{
		Name: "PRIMARY", Type: model.PrimaryKeyConstraint,
		Definition: "(id)", Columns: []string{"id"},
	}
}

// table rename --------------------------------------------------------------

func TestDiffRenameTableEmitsAlter(t *testing.T) {
	current := orderedmap.New[string, *model.Table]()
	desired := orderedmap.New[string, *model.Table]()

	old := tbl("shop", "old_users")
	old.Columns.Set("id", col("id", "bigint"))
	old.Constraints.Set("PRIMARY", pkConstraint())
	current.Set("shop.old_users", old)

	from := "old_users"
	new := tbl("shop", "users")
	new.Columns.Set("id", col("id", "bigint"))
	new.Constraints.Set("PRIMARY", pkConstraint())
	new.RenameFrom = &from
	desired.Set("shop.users", new)

	res, err := diff.DiffTables(current, desired, allowAll)
	require.NoError(t, err)

	got := strings.Join(res.RenameStmts, "\n")
	assert.Contains(t, got, "ALTER TABLE shop.old_users RENAME TO shop.users;")
	all := strings.Join(append(append([]string{}, res.Stmts...), res.DropStmts...), "\n")
	assert.NotContains(t, all, "DROP TABLE", "rename must not surface as drop")
	assert.NotContains(t, all, "CREATE TABLE", "rename must not surface as create")
}

func TestDiffRenameTableAlsoRewritesCrossTableFKRefs(t *testing.T) {
	// Renaming `shop.users` → `shop.members` while another table
	// (`shop.posts`) holds an FK to `shop.users(id)` must NOT diff that
	// FK as DROP+ADD just because RefTable changed name. The rename
	// pass needs to walk *all* tables and rewrite (RefDB, RefTable) on
	// matching FKs.
	current := orderedmap.New[string, *model.Table]()
	desired := orderedmap.New[string, *model.Table]()

	// shop.users — being renamed
	curUsers := tbl("shop", "users")
	curUsers.Columns.Set("id", col("id", "bigint"))
	curUsers.Constraints.Set("PRIMARY", pkConstraint())
	current.Set("shop.users", curUsers)

	// shop.posts — references shop.users via FK
	curPosts := tbl("shop", "posts")
	curPosts.Columns.Set("id", col("id", "bigint"))
	curPosts.Columns.Set("user_id", col("user_id", "bigint"))
	curPosts.Constraints.Set("PRIMARY", pkConstraint())
	curPosts.ForeignKeys.Set("fk_user", &model.ForeignKey{
		Name: "fk_user", Database: "shop", Table: "posts",
		Columns: []string{"user_id"},
		RefDB:   "shop", RefTable: "users", RefCols: []string{"id"},
	})
	current.Set("shop.posts", curPosts)

	// desired: shop.users → shop.members; shop.posts FK now points at shop.members
	from := "users"
	desUsers := tbl("shop", "members")
	desUsers.Columns.Set("id", col("id", "bigint"))
	desUsers.Constraints.Set("PRIMARY", pkConstraint())
	desUsers.RenameFrom = &from
	desired.Set("shop.members", desUsers)

	desPosts := tbl("shop", "posts")
	desPosts.Columns.Set("id", col("id", "bigint"))
	desPosts.Columns.Set("user_id", col("user_id", "bigint"))
	desPosts.Constraints.Set("PRIMARY", pkConstraint())
	desPosts.ForeignKeys.Set("fk_user", &model.ForeignKey{
		Name: "fk_user", Database: "shop", Table: "posts",
		Columns: []string{"user_id"},
		RefDB:   "shop", RefTable: "members", RefCols: []string{"id"},
	})
	desired.Set("shop.posts", desPosts)

	res, err := diff.DiffTables(current, desired, allowAll)
	require.NoError(t, err)

	allRen := strings.Join(res.RenameStmts, "\n")
	allFK := strings.Join(append(append([]string{}, res.FKDropStmts...), res.FKAddStmts...), "\n")
	assert.Contains(t, allRen, "ALTER TABLE shop.users RENAME TO shop.members;")
	assert.NotContains(t, allFK, "DROP FOREIGN KEY", "FK on referencing table must not be dropped after rename")
	assert.NotContains(t, allFK, "ADD CONSTRAINT", "FK on referencing table must not be re-added after rename")
}

func TestDiffRenameSelfRenameIsNoOp(t *testing.T) {
	// Directive lists the desired table's own name as the source — a
	// typo that, if not guarded, would emit
	// `ALTER TABLE x RENAME TO x;` (rejected on some MySQL versions
	// and a no-op on others). Same guard applies to columns and
	// indexes via applyColumnRenames / applyIndexRenames.
	current := orderedmap.New[string, *model.Table]()
	desired := orderedmap.New[string, *model.Table]()

	cur := tbl("shop", "users")
	cur.Columns.Set("id", col("id", "bigint"))
	cur.Columns.Set("name", col("name", "varchar(64)"))
	cur.Constraints.Set("PRIMARY", pkConstraint())
	cur.Indexes.Set("idx_x", &model.Index{
		Name: "idx_x", Database: "shop", Table: "users",
		Parts: []model.IndexPart{{Column: "name"}},
	})
	current.Set("shop.users", cur)

	want := tbl("shop", "users")
	tableSelf := "users"
	want.RenameFrom = &tableSelf
	want.Columns.Set("id", col("id", "bigint"))
	colSelf := "name"
	wcol := col("name", "varchar(64)")
	wcol.RenameFrom = &colSelf
	want.Columns.Set("name", wcol)
	want.Constraints.Set("PRIMARY", pkConstraint())
	idxSelf := "idx_x"
	want.Indexes.Set("idx_x", &model.Index{
		Name: "idx_x", Database: "shop", Table: "users",
		Parts:      []model.IndexPart{{Column: "name"}},
		RenameFrom: &idxSelf,
	})
	desired.Set("shop.users", want)

	res, err := diff.DiffTables(current, desired, allowAll)
	require.NoError(t, err)
	all := strings.Join(append(append([]string{}, res.RenameStmts...), res.Stmts...), "\n")
	assert.NotContains(t, all, "RENAME TO", "table self-rename must not emit ALTER TABLE x RENAME TO x")
	assert.NotContains(t, all, "RENAME COLUMN", "column self-rename must not emit RENAME COLUMN x TO x")
	assert.NotContains(t, all, "RENAME INDEX", "index self-rename must not emit RENAME INDEX x TO x")
}

func TestDiffRenameColumnPreservesPKPrefixLengthAndDesc(t *testing.T) {
	// PK column has both a prefix length and DESC modifier. After
	// renaming the column, the rebuilt PK Definition must still carry
	// `(new_col(10) DESC)` — not just `(new_col)`. Otherwise current
	// (rebuilt as bare names) and desired (with full modifiers) diverge
	// and diffConstraints fires DROP+ADD PRIMARY KEY.
	current := orderedmap.New[string, *model.Table]()
	desired := orderedmap.New[string, *model.Table]()

	cur := tbl("shop", "users")
	cur.Columns.Set("old_key", col("old_key", "varchar(64)"))
	cur.Constraints.Set("PRIMARY", &model.Constraint{
		Name: "PRIMARY", Type: model.PrimaryKeyConstraint,
		Definition: "(old_key(10) DESC)", Columns: []string{"old_key"},
	})
	cur.Indexes.Set("PRIMARY", &model.Index{
		Name: "PRIMARY", Database: "shop", Table: "users", Primary: true,
		Parts: []model.IndexPart{{Column: "old_key", Length: 10, Desc: true}},
	})
	current.Set("shop.users", cur)

	from := "old_key"
	want := tbl("shop", "users")
	wc := col("user_key", "varchar(64)")
	wc.RenameFrom = &from
	want.Columns.Set("user_key", wc)
	want.Constraints.Set("PRIMARY", &model.Constraint{
		Name: "PRIMARY", Type: model.PrimaryKeyConstraint,
		Definition: "(user_key(10) DESC)", Columns: []string{"user_key"},
	})
	want.Indexes.Set("PRIMARY", &model.Index{
		Name: "PRIMARY", Database: "shop", Table: "users", Primary: true,
		Parts: []model.IndexPart{{Column: "user_key", Length: 10, Desc: true}},
	})
	desired.Set("shop.users", want)

	res, err := diff.DiffTables(current, desired, allowAll)
	require.NoError(t, err)
	got := strings.Join(res.Stmts, "\n")
	assert.Contains(t, got, "RENAME COLUMN old_key TO user_key")
	assert.NotContains(t, got, "DROP PRIMARY KEY",
		"PK Definition rebuild must keep prefix length + DESC so diff stays quiet")
	assert.NotContains(t, got, "ADD PRIMARY KEY",
		"PK Definition rebuild must keep prefix length + DESC so diff stays quiet")
}

func TestDiffRenameColumnAlsoRewritesPKConstraint(t *testing.T) {
	// Rename a PK column. Without rewriting the PRIMARY KEY constraint
	// in current, diffConstraints would see (old) vs (new) and emit
	// DROP PRIMARY KEY + ADD PRIMARY KEY — even though MySQL updates
	// the PK metadata automatically alongside RENAME COLUMN.
	current := orderedmap.New[string, *model.Table]()
	desired := orderedmap.New[string, *model.Table]()

	cur := tbl("shop", "users")
	cur.Columns.Set("old_id", col("old_id", "bigint"))
	cur.Constraints.Set("PRIMARY", &model.Constraint{
		Name: "PRIMARY", Type: model.PrimaryKeyConstraint,
		Definition: "(old_id)", Columns: []string{"old_id"},
	})
	current.Set("shop.users", cur)

	want := tbl("shop", "users")
	from := "old_id"
	wc := col("id", "bigint")
	wc.RenameFrom = &from
	want.Columns.Set("id", wc)
	want.Constraints.Set("PRIMARY", &model.Constraint{
		Name: "PRIMARY", Type: model.PrimaryKeyConstraint,
		Definition: "(id)", Columns: []string{"id"},
	})
	desired.Set("shop.users", want)

	res, err := diff.DiffTables(current, desired, allowAll)
	require.NoError(t, err)
	got := strings.Join(res.Stmts, "\n")
	assert.Contains(t, got, "RENAME COLUMN old_id TO id")
	assert.NotContains(t, got, "DROP PRIMARY KEY", "PK rewrite should suppress DROP PRIMARY KEY")
	assert.NotContains(t, got, "ADD PRIMARY KEY", "PK rewrite should suppress ADD PRIMARY KEY")
}

func TestDiffRenameColumnAlsoRewritesCrossTableFKRefCols(t *testing.T) {
	// Renaming `users.id` → `users.user_id` while another table
	// (`posts`) holds an FK to `users(id)` must NOT diff that FK as
	// DROP+ADD just because RefCols changed name. The parent-side
	// rename pass needs to walk all tables and rewrite RefCols on
	// matching FKs.
	current := orderedmap.New[string, *model.Table]()
	desired := orderedmap.New[string, *model.Table]()

	// users — column is being renamed
	curUsers := tbl("shop", "users")
	curUsers.Columns.Set("id", col("id", "bigint"))
	curUsers.Constraints.Set("PRIMARY", &model.Constraint{
		Name: "PRIMARY", Type: model.PrimaryKeyConstraint,
		Definition: "(id)", Columns: []string{"id"},
	})
	current.Set("shop.users", curUsers)

	// posts — references users(id)
	curPosts := tbl("shop", "posts")
	curPosts.Columns.Set("id", col("id", "bigint"))
	curPosts.Columns.Set("user_id", col("user_id", "bigint"))
	curPosts.Constraints.Set("PRIMARY", pkConstraint())
	curPosts.ForeignKeys.Set("fk_user", &model.ForeignKey{
		Name: "fk_user", Database: "shop", Table: "posts",
		Columns: []string{"user_id"},
		RefDB:   "shop", RefTable: "users", RefCols: []string{"id"},
	})
	current.Set("shop.posts", curPosts)

	// desired: rename users.id → users.user_id; posts FK now points at user_id
	from := "id"
	desUsers := tbl("shop", "users")
	wc := col("user_id", "bigint")
	wc.RenameFrom = &from
	desUsers.Columns.Set("user_id", wc)
	desUsers.Constraints.Set("PRIMARY", &model.Constraint{
		Name: "PRIMARY", Type: model.PrimaryKeyConstraint,
		Definition: "(user_id)", Columns: []string{"user_id"},
	})
	desired.Set("shop.users", desUsers)

	desPosts := tbl("shop", "posts")
	desPosts.Columns.Set("id", col("id", "bigint"))
	desPosts.Columns.Set("user_id", col("user_id", "bigint"))
	desPosts.Constraints.Set("PRIMARY", pkConstraint())
	desPosts.ForeignKeys.Set("fk_user", &model.ForeignKey{
		Name: "fk_user", Database: "shop", Table: "posts",
		Columns: []string{"user_id"},
		RefDB:   "shop", RefTable: "users", RefCols: []string{"user_id"},
	})
	desired.Set("shop.posts", desPosts)

	res, err := diff.DiffTables(current, desired, allowAll)
	require.NoError(t, err)
	got := strings.Join(res.Stmts, "\n")
	allFK := strings.Join(append(append([]string{}, res.FKDropStmts...), res.FKAddStmts...), "\n")
	assert.Contains(t, got, "RENAME COLUMN id TO user_id")
	assert.NotContains(t, allFK, "DROP FOREIGN KEY", "FK on referencing table must not be dropped after parent column rename")
	assert.NotContains(t, allFK, "ADD CONSTRAINT", "FK on referencing table must not be re-added after parent column rename")
}

func TestDiffRenameColumnAlsoRewritesSelfReferentialFK(t *testing.T) {
	// Self-referential FK: `users.parent_id` references `users.id`
	// (same table). When `users.id` is renamed to `users.user_id`,
	// rewriteFKColumnRefs must rewrite both fk.Columns (child side)
	// and fk.RefCols (parent side, which here is the same table).
	// Without the self-ref guard, fkEqual would diff RefCols and
	// surface a destructive DROP+ADD.
	current := orderedmap.New[string, *model.Table]()
	desired := orderedmap.New[string, *model.Table]()

	cur := tbl("shop", "users")
	cur.Columns.Set("id", col("id", "bigint"))
	cur.Columns.Set("parent_id", col("parent_id", "bigint"))
	cur.Constraints.Set("PRIMARY", &model.Constraint{
		Name: "PRIMARY", Type: model.PrimaryKeyConstraint,
		Definition: "(id)", Columns: []string{"id"},
	})
	cur.ForeignKeys.Set("fk_parent", &model.ForeignKey{
		Name: "fk_parent", Database: "shop", Table: "users",
		Columns: []string{"parent_id"},
		RefDB:   "shop", RefTable: "users", RefCols: []string{"id"},
	})
	current.Set("shop.users", cur)

	from := "id"
	want := tbl("shop", "users")
	wc := col("user_id", "bigint")
	wc.RenameFrom = &from
	want.Columns.Set("user_id", wc)
	want.Columns.Set("parent_id", col("parent_id", "bigint"))
	want.Constraints.Set("PRIMARY", &model.Constraint{
		Name: "PRIMARY", Type: model.PrimaryKeyConstraint,
		Definition: "(user_id)", Columns: []string{"user_id"},
	})
	want.ForeignKeys.Set("fk_parent", &model.ForeignKey{
		Name: "fk_parent", Database: "shop", Table: "users",
		Columns: []string{"parent_id"},
		RefDB:   "shop", RefTable: "users", RefCols: []string{"user_id"},
	})
	desired.Set("shop.users", want)

	res, err := diff.DiffTables(current, desired, allowAll)
	require.NoError(t, err)
	got := strings.Join(res.Stmts, "\n")
	allFK := strings.Join(append(append([]string{}, res.FKDropStmts...), res.FKAddStmts...), "\n")
	assert.Contains(t, got, "RENAME COLUMN id TO user_id")
	assert.NotContains(t, allFK, "DROP FOREIGN KEY", "self-referential FK must not be dropped")
	assert.NotContains(t, allFK, "ADD CONSTRAINT", "self-referential FK must not be re-added")
}

func TestDiffRenameSourceAlsoDeclaredErrors(t *testing.T) {
	// Desired schema declares both the source column (`legacy`) and the
	// destination column (`name`, with renamed-from legacy). Without
	// the guard, the rename would happen and then diffColumns would
	// re-add `legacy` — almost certainly not what the user meant.
	current := orderedmap.New[string, *model.Table]()
	desired := orderedmap.New[string, *model.Table]()

	cur := tbl("shop", "users")
	cur.Columns.Set("id", col("id", "bigint"))
	cur.Columns.Set("legacy", col("legacy", "varchar(64)"))
	cur.Constraints.Set("PRIMARY", pkConstraint())
	current.Set("shop.users", cur)

	from := "legacy"
	want := tbl("shop", "users")
	want.Columns.Set("id", col("id", "bigint"))
	want.Columns.Set("legacy", col("legacy", "varchar(64)")) // source still declared
	renamed := col("name", "varchar(64)")
	renamed.RenameFrom = &from
	want.Columns.Set("name", renamed)
	want.Constraints.Set("PRIMARY", pkConstraint())
	desired.Set("shop.users", want)

	_, err := diff.DiffTables(current, desired, allowAll)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "renamed-from")
	assert.Contains(t, err.Error(), "also declared")
}

func TestDiffRenameDuplicateSourceErrors(t *testing.T) {
	// Two desired columns claim the same RenameFrom source. Without
	// the pre-validation, the second one would surface as a confusing
	// "source not found" after the first rename mutated current.
	current := orderedmap.New[string, *model.Table]()
	desired := orderedmap.New[string, *model.Table]()

	cur := tbl("shop", "users")
	cur.Columns.Set("id", col("id", "bigint"))
	cur.Columns.Set("legacy", col("legacy", "varchar(64)"))
	cur.Constraints.Set("PRIMARY", pkConstraint())
	current.Set("shop.users", cur)

	from := "legacy"
	want := tbl("shop", "users")
	want.Columns.Set("id", col("id", "bigint"))
	a := col("name", "varchar(64)")
	a.RenameFrom = &from
	want.Columns.Set("name", a)
	b := col("alias", "varchar(64)")
	b.RenameFrom = &from
	want.Columns.Set("alias", b)
	want.Constraints.Set("PRIMARY", pkConstraint())
	desired.Set("shop.users", want)

	_, err := diff.DiffTables(current, desired, allowAll)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "renamed-from")
	assert.Contains(t, err.Error(), "multiple")
}

func TestDiffRenameTableMissingSourceErrors(t *testing.T) {
	current := orderedmap.New[string, *model.Table]()
	desired := orderedmap.New[string, *model.Table]()

	from := "ghost"
	new := tbl("shop", "users")
	new.RenameFrom = &from
	desired.Set("shop.users", new)

	_, err := diff.DiffTables(current, desired, allowAll)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "renamed-from")
	assert.Contains(t, err.Error(), "ghost")
}

func TestDiffRenameTableIdempotentWhenAlreadyRenamed(t *testing.T) {
	// Re-applying a desired SQL after the rename has already happened
	// should be a no-op, not an error.
	current := orderedmap.New[string, *model.Table]()
	desired := orderedmap.New[string, *model.Table]()

	already := tbl("shop", "users")
	already.Columns.Set("id", col("id", "bigint"))
	already.Constraints.Set("PRIMARY", pkConstraint())
	current.Set("shop.users", already)

	from := "old_users"
	new := tbl("shop", "users")
	new.Columns.Set("id", col("id", "bigint"))
	new.Constraints.Set("PRIMARY", pkConstraint())
	new.RenameFrom = &from
	desired.Set("shop.users", new)

	res, err := diff.DiffTables(current, desired, allowAll)
	require.NoError(t, err)
	assert.Empty(t, res.Stmts, "re-apply of an already-renamed table should be a no-op")
}

// column rename -------------------------------------------------------------

func TestDiffRenameColumnEmitsAlter(t *testing.T) {
	current := orderedmap.New[string, *model.Table]()
	desired := orderedmap.New[string, *model.Table]()

	cur := tbl("shop", "users")
	cur.Columns.Set("id", col("id", "bigint"))
	cur.Columns.Set("old_name", col("old_name", "varchar(64)"))
	cur.Constraints.Set("PRIMARY", pkConstraint())
	current.Set("shop.users", cur)

	want := tbl("shop", "users")
	want.Columns.Set("id", col("id", "bigint"))
	from := "old_name"
	renamed := col("name", "varchar(64)")
	renamed.RenameFrom = &from
	want.Columns.Set("name", renamed)
	want.Constraints.Set("PRIMARY", pkConstraint())
	desired.Set("shop.users", want)

	res, err := diff.DiffTables(current, desired, allowAll)
	require.NoError(t, err)

	got := strings.Join(res.Stmts, "\n")
	assert.Contains(t, got, "ALTER TABLE shop.users RENAME COLUMN old_name TO name;")
	assert.NotContains(t, got, "DROP COLUMN", "rename must not show as drop")
	assert.NotContains(t, got, "ADD COLUMN", "rename must not show as add")
}

func TestDiffRenameColumnAlsoRewritesIndexParts(t *testing.T) {
	// Index in current side covers `old_col`; desired side covers
	// `new_col` and the column has renamed-from old_col. Without
	// rewrite, indexEqual would see a mismatch and emit DROP+CREATE
	// of the index; with rewrite, the index is unchanged.
	current := orderedmap.New[string, *model.Table]()
	desired := orderedmap.New[string, *model.Table]()

	cur := tbl("shop", "posts")
	cur.Columns.Set("id", col("id", "bigint"))
	cur.Columns.Set("old_col", col("old_col", "bigint"))
	cur.Constraints.Set("PRIMARY", pkConstraint())
	cur.Indexes.Set("idx_x", &model.Index{
		Name: "idx_x", Database: "shop", Table: "posts",
		Parts: []model.IndexPart{{Column: "old_col"}},
	})
	current.Set("shop.posts", cur)

	want := tbl("shop", "posts")
	want.Columns.Set("id", col("id", "bigint"))
	from := "old_col"
	renamed := col("new_col", "bigint")
	renamed.RenameFrom = &from
	want.Columns.Set("new_col", renamed)
	want.Constraints.Set("PRIMARY", pkConstraint())
	want.Indexes.Set("idx_x", &model.Index{
		Name: "idx_x", Database: "shop", Table: "posts",
		Parts: []model.IndexPart{{Column: "new_col"}},
	})
	desired.Set("shop.posts", want)

	res, err := diff.DiffTables(current, desired, allowAll)
	require.NoError(t, err)

	got := strings.Join(res.Stmts, "\n")
	assert.Contains(t, got, "RENAME COLUMN old_col TO new_col")
	assert.NotContains(t, got, "DROP INDEX", "index should not be touched after column rename")
	assert.NotContains(t, got, "CREATE INDEX", "index should not be recreated after column rename")
}

// index rename --------------------------------------------------------------

func TestDiffRenameIndexEmitsAlter(t *testing.T) {
	current := orderedmap.New[string, *model.Table]()
	desired := orderedmap.New[string, *model.Table]()

	cur := tbl("shop", "posts")
	cur.Columns.Set("id", col("id", "bigint"))
	cur.Columns.Set("user_id", col("user_id", "bigint"))
	cur.Constraints.Set("PRIMARY", pkConstraint())
	cur.Indexes.Set("old_idx", &model.Index{
		Name: "old_idx", Database: "shop", Table: "posts",
		Parts: []model.IndexPart{{Column: "user_id"}},
	})
	current.Set("shop.posts", cur)

	want := tbl("shop", "posts")
	want.Columns.Set("id", col("id", "bigint"))
	want.Columns.Set("user_id", col("user_id", "bigint"))
	want.Constraints.Set("PRIMARY", pkConstraint())
	from := "old_idx"
	want.Indexes.Set("new_idx", &model.Index{
		Name: "new_idx", Database: "shop", Table: "posts",
		Parts:      []model.IndexPart{{Column: "user_id"}},
		RenameFrom: &from,
	})
	desired.Set("shop.posts", want)

	res, err := diff.DiffTables(current, desired, allowAll)
	require.NoError(t, err)

	got := strings.Join(res.Stmts, "\n")
	assert.Contains(t, got, "ALTER TABLE shop.posts RENAME INDEX old_idx TO new_idx;")
	assert.NotContains(t, got, "DROP INDEX")
	assert.NotContains(t, got, "CREATE INDEX")
}

func TestDiffRenameColumnAlsoRewritesFKColumns(t *testing.T) {
	// Same idea as the index-rewrite test, but for the FK side. After
	// renaming `posts.author_id` → `posts.user_id`, the FK that
	// references the renamed column on this table should NOT show as a
	// DROP FOREIGN KEY + ADD CONSTRAINT — only the column rename
	// should fire.
	current := orderedmap.New[string, *model.Table]()
	desired := orderedmap.New[string, *model.Table]()

	cur := tbl("shop", "posts")
	cur.Columns.Set("id", col("id", "bigint"))
	cur.Columns.Set("author_id", col("author_id", "bigint"))
	cur.Constraints.Set("PRIMARY", pkConstraint())
	cur.ForeignKeys.Set("fk_author", &model.ForeignKey{
		Name: "fk_author", Database: "shop", Table: "posts",
		Columns: []string{"author_id"},
		RefDB:   "shop", RefTable: "users", RefCols: []string{"id"},
	})
	current.Set("shop.posts", cur)

	want := tbl("shop", "posts")
	want.Columns.Set("id", col("id", "bigint"))
	from := "author_id"
	renamed := col("user_id", "bigint")
	renamed.RenameFrom = &from
	want.Columns.Set("user_id", renamed)
	want.Constraints.Set("PRIMARY", pkConstraint())
	want.ForeignKeys.Set("fk_author", &model.ForeignKey{
		Name: "fk_author", Database: "shop", Table: "posts",
		Columns: []string{"user_id"},
		RefDB:   "shop", RefTable: "users", RefCols: []string{"id"},
	})
	desired.Set("shop.posts", want)

	res, err := diff.DiffTables(current, desired, allowAll)
	require.NoError(t, err)

	got := strings.Join(res.Stmts, "\n")
	gotFK := strings.Join(append(append([]string{}, res.FKDropStmts...), res.FKAddStmts...), "\n")
	assert.Contains(t, got, "RENAME COLUMN author_id TO user_id")
	assert.NotContains(t, gotFK, "DROP FOREIGN KEY", "FK must not be dropped after column rename")
	assert.NotContains(t, gotFK, "ADD CONSTRAINT", "FK must not be re-added after column rename")
}

func TestDiffRenameIndexMissingSourceErrors(t *testing.T) {
	current := orderedmap.New[string, *model.Table]()
	desired := orderedmap.New[string, *model.Table]()

	cur := tbl("shop", "posts")
	cur.Columns.Set("id", col("id", "bigint"))
	cur.Constraints.Set("PRIMARY", pkConstraint())
	current.Set("shop.posts", cur)

	want := tbl("shop", "posts")
	want.Columns.Set("id", col("id", "bigint"))
	want.Constraints.Set("PRIMARY", pkConstraint())
	from := "ghost"
	want.Indexes.Set("new_idx", &model.Index{
		Name: "new_idx", Database: "shop", Table: "posts",
		Parts:      []model.IndexPart{{Column: "id"}},
		RenameFrom: &from,
	})
	desired.Set("shop.posts", want)

	_, err := diff.DiffTables(current, desired, allowAll)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "renamed-from")
	assert.Contains(t, err.Error(), "ghost")
}

// gap-coverage tests --------------------------------------------------------
// Each test below was added after a coverage audit identified an edge case
// that the renamed-from path supports in code but had no dedicated test.

// Gap 1: parent-side column rename when the referencing FK is multi-column
// and the renamed column appears at a non-first position. The cross-table
// RefCols rewrite walks each entry by index, so position shouldn't matter,
// but only single-column RefCols had a test before this.
func TestDiffRenameColumnRewritesMultiColFKRefColsAtNonFirstPosition(t *testing.T) {
	current := orderedmap.New[string, *model.Table]()
	desired := orderedmap.New[string, *model.Table]()

	curParent := tbl("shop", "users")
	curParent.Columns.Set("org_id", col("org_id", "bigint"))
	curParent.Columns.Set("id", col("id", "bigint"))
	curParent.Constraints.Set("PRIMARY", &model.Constraint{
		Name: "PRIMARY", Type: model.PrimaryKeyConstraint,
		Definition: "(org_id, id)", Columns: []string{"org_id", "id"},
	})
	curParent.Indexes.Set("PRIMARY", &model.Index{
		Name: "PRIMARY", Database: "shop", Table: "users", Primary: true,
		Parts: []model.IndexPart{{Column: "org_id"}, {Column: "id"}},
	})
	current.Set("shop.users", curParent)

	curChild := tbl("shop", "posts")
	curChild.Columns.Set("id", col("id", "bigint"))
	curChild.Columns.Set("org_id", col("org_id", "bigint"))
	curChild.Columns.Set("user_id", col("user_id", "bigint"))
	curChild.Constraints.Set("PRIMARY", pkConstraint())
	curChild.ForeignKeys.Set("fk_user", &model.ForeignKey{
		Name: "fk_user", Database: "shop", Table: "posts",
		Columns: []string{"org_id", "user_id"},
		RefDB:   "shop", RefTable: "users", RefCols: []string{"org_id", "id"},
	})
	current.Set("shop.posts", curChild)

	from := "id"
	desParent := tbl("shop", "users")
	desParent.Columns.Set("org_id", col("org_id", "bigint"))
	wc := col("user_id", "bigint")
	wc.RenameFrom = &from
	desParent.Columns.Set("user_id", wc)
	desParent.Constraints.Set("PRIMARY", &model.Constraint{
		Name: "PRIMARY", Type: model.PrimaryKeyConstraint,
		Definition: "(org_id, user_id)", Columns: []string{"org_id", "user_id"},
	})
	desParent.Indexes.Set("PRIMARY", &model.Index{
		Name: "PRIMARY", Database: "shop", Table: "users", Primary: true,
		Parts: []model.IndexPart{{Column: "org_id"}, {Column: "user_id"}},
	})
	desired.Set("shop.users", desParent)

	desChild := tbl("shop", "posts")
	desChild.Columns.Set("id", col("id", "bigint"))
	desChild.Columns.Set("org_id", col("org_id", "bigint"))
	desChild.Columns.Set("user_id", col("user_id", "bigint"))
	desChild.Constraints.Set("PRIMARY", pkConstraint())
	desChild.ForeignKeys.Set("fk_user", &model.ForeignKey{
		Name: "fk_user", Database: "shop", Table: "posts",
		Columns: []string{"org_id", "user_id"},
		RefDB:   "shop", RefTable: "users", RefCols: []string{"org_id", "user_id"},
	})
	desired.Set("shop.posts", desChild)

	res, err := diff.DiffTables(current, desired, allowAll)
	require.NoError(t, err)
	allFK := strings.Join(append(append([]string{}, res.FKDropStmts...), res.FKAddStmts...), "\n")
	assert.NotContains(t, allFK, "DROP FOREIGN KEY", "multi-col FK with renamed second RefCol must not be dropped")
	assert.NotContains(t, allFK, "ADD CONSTRAINT", "multi-col FK with renamed second RefCol must not be re-added")
}

// Gap 2: ALTER TABLE … RENAME INDEX must preserve per-part modifiers
// (prefix length, DESC). The rename path only touches Name + map-key, so
// Parts come along for the ride; this test pins that invariant.
func TestDiffRenameIndexPreservesPrefixLengthAndDesc(t *testing.T) {
	current := orderedmap.New[string, *model.Table]()
	desired := orderedmap.New[string, *model.Table]()

	cur := tbl("shop", "posts")
	cur.Columns.Set("id", col("id", "bigint"))
	cur.Columns.Set("title", col("title", "varchar(255)"))
	cur.Constraints.Set("PRIMARY", pkConstraint())
	cur.Indexes.Set("old_idx", &model.Index{
		Name: "old_idx", Database: "shop", Table: "posts",
		Parts: []model.IndexPart{{Column: "title", Length: 16, Desc: true}},
	})
	current.Set("shop.posts", cur)

	want := tbl("shop", "posts")
	want.Columns.Set("id", col("id", "bigint"))
	want.Columns.Set("title", col("title", "varchar(255)"))
	want.Constraints.Set("PRIMARY", pkConstraint())
	from := "old_idx"
	want.Indexes.Set("idx_title", &model.Index{
		Name: "idx_title", Database: "shop", Table: "posts",
		Parts:      []model.IndexPart{{Column: "title", Length: 16, Desc: true}},
		RenameFrom: &from,
	})
	desired.Set("shop.posts", want)

	res, err := diff.DiffTables(current, desired, allowAll)
	require.NoError(t, err)
	got := strings.Join(res.Stmts, "\n")
	assert.Contains(t, got, "RENAME INDEX old_idx TO idx_title")
	assert.NotContains(t, got, "DROP INDEX", "prefix-length/DESC parts must survive the rename")
	assert.NotContains(t, got, "CREATE INDEX", "prefix-length/DESC parts must survive the rename")
}

// Gap 3: dropping only some columns of a multi-column index must NOT
// trigger the "all parts dropped → suppress DROP INDEX" optimisation.
// The optimisation hinges on MySQL auto-removing the index when its
// last column goes; with surviving columns the index stays, and the
// explicit DROP INDEX is required to match desired (no index).
func TestDiffDropPartialColumnEmitsDropIndex(t *testing.T) {
	current := orderedmap.New[string, *model.Table]()
	desired := orderedmap.New[string, *model.Table]()

	cur := tbl("shop", "posts")
	cur.Columns.Set("id", col("id", "bigint"))
	cur.Columns.Set("a", col("a", "bigint"))
	cur.Columns.Set("b", col("b", "bigint"))
	cur.Constraints.Set("PRIMARY", pkConstraint())
	cur.Indexes.Set("ab", &model.Index{
		Name: "ab", Database: "shop", Table: "posts",
		Parts: []model.IndexPart{{Column: "a"}, {Column: "b"}},
	})
	current.Set("shop.posts", cur)

	want := tbl("shop", "posts")
	want.Columns.Set("id", col("id", "bigint"))
	want.Columns.Set("b", col("b", "bigint"))
	want.Constraints.Set("PRIMARY", pkConstraint())
	desired.Set("shop.posts", want)

	res, err := diff.DiffTables(current, desired, allowAll)
	require.NoError(t, err)
	got := strings.Join(res.Stmts, "\n")
	assert.Contains(t, got, "DROP COLUMN a")
	assert.Contains(t, got, "DROP INDEX ab",
		"partial-column drop on a multi-col index must still DROP INDEX (suppression doesn't apply)")
}

// Gap 4: self-referential FK whose RefCols includes the renamed column.
// rewriteFKColumnRefs walks both Columns (always) and RefCols (only when
// the FK's parent is this same table). Without that self-ref branch the
// FK would diff as DROP+ADD after `id` → `new_id`.
func TestDiffRenameColumnRewritesSelfRefFKRefCols(t *testing.T) {
	current := orderedmap.New[string, *model.Table]()
	desired := orderedmap.New[string, *model.Table]()

	cur := tbl("shop", "tree")
	cur.Columns.Set("id", col("id", "bigint"))
	cur.Columns.Set("parent_id", col("parent_id", "bigint"))
	cur.Constraints.Set("PRIMARY", pkConstraint())
	cur.ForeignKeys.Set("fk_parent", &model.ForeignKey{
		Name: "fk_parent", Database: "shop", Table: "tree",
		Columns: []string{"parent_id"},
		RefDB:   "shop", RefTable: "tree", RefCols: []string{"id"},
	})
	current.Set("shop.tree", cur)

	from := "id"
	want := tbl("shop", "tree")
	wc := col("new_id", "bigint")
	wc.RenameFrom = &from
	want.Columns.Set("new_id", wc)
	want.Columns.Set("parent_id", col("parent_id", "bigint"))
	want.Constraints.Set("PRIMARY", &model.Constraint{
		Name: "PRIMARY", Type: model.PrimaryKeyConstraint,
		Definition: "(new_id)", Columns: []string{"new_id"},
	})
	want.ForeignKeys.Set("fk_parent", &model.ForeignKey{
		Name: "fk_parent", Database: "shop", Table: "tree",
		Columns: []string{"parent_id"},
		RefDB:   "shop", RefTable: "tree", RefCols: []string{"new_id"},
	})
	desired.Set("shop.tree", want)

	res, err := diff.DiffTables(current, desired, allowAll)
	require.NoError(t, err)
	got := strings.Join(res.Stmts, "\n")
	allFK := strings.Join(append(append([]string{}, res.FKDropStmts...), res.FKAddStmts...), "\n")
	assert.Contains(t, got, "RENAME COLUMN id TO new_id")
	assert.NotContains(t, allFK, "DROP FOREIGN KEY", "self-ref FK must not drop on column rename")
	assert.NotContains(t, allFK, "ADD CONSTRAINT", "self-ref FK must not be re-added on column rename")
}

// Gap 5: in a single plan, a table rename and an FK drop on (what
// becomes) the renamed table must end up in the right buckets.
// RenameStmts is its own bucket and diff_all.go schedules it ahead of
// FKDropStmts; this test pins the bucket separation at the diff layer
// so a future shuffle in tables.go fails loudly.
func TestDiffRenameTableSeparatesFromFKDropOnSameTable(t *testing.T) {
	current := orderedmap.New[string, *model.Table]()
	desired := orderedmap.New[string, *model.Table]()

	curParent := tbl("shop", "users")
	curParent.Columns.Set("id", col("id", "bigint"))
	curParent.Constraints.Set("PRIMARY", pkConstraint())
	current.Set("shop.users", curParent)

	curChild := tbl("shop", "posts")
	curChild.Columns.Set("id", col("id", "bigint"))
	curChild.Columns.Set("user_id", col("user_id", "bigint"))
	curChild.Constraints.Set("PRIMARY", pkConstraint())
	curChild.ForeignKeys.Set("fk_user", &model.ForeignKey{
		Name: "fk_user", Database: "shop", Table: "posts",
		Columns: []string{"user_id"},
		RefDB:   "shop", RefTable: "users", RefCols: []string{"id"},
	})
	current.Set("shop.posts", curChild)

	desParent := tbl("shop", "users")
	desParent.Columns.Set("id", col("id", "bigint"))
	desParent.Constraints.Set("PRIMARY", pkConstraint())
	desired.Set("shop.users", desParent)

	from := "posts"
	desChild := tbl("shop", "comments")
	desChild.RenameFrom = &from
	desChild.Columns.Set("id", col("id", "bigint"))
	desChild.Columns.Set("user_id", col("user_id", "bigint"))
	desChild.Constraints.Set("PRIMARY", pkConstraint())
	desired.Set("shop.comments", desChild)

	res, err := diff.DiffTables(current, desired, allowAll)
	require.NoError(t, err)
	require.NotEmpty(t, res.RenameStmts, "table rename must land in RenameStmts bucket")
	require.NotEmpty(t, res.FKDropStmts, "FK on renamed table must be in FKDropStmts")
	assert.Contains(t, res.RenameStmts[0], "ALTER TABLE shop.posts RENAME TO shop.comments")
	// Post-rename, current is re-keyed under the new name, so the FK
	// drop statement targets the new table name. Apply order
	// (RenameStmts before FKDropStmts in diff_all.go) makes that the
	// table that exists at the moment the DROP runs.
	assert.Contains(t, res.FKDropStmts[0], "ALTER TABLE shop.comments DROP FOREIGN KEY fk_user")
}
