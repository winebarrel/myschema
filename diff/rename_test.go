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

	got := strings.Join(res.Stmts, "\n")
	assert.Contains(t, got, "ALTER TABLE shop.old_users RENAME TO shop.users;")
	assert.NotContains(t, got, "DROP TABLE", "rename must not surface as drop")
	assert.NotContains(t, got, "CREATE TABLE", "rename must not surface as create")
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

	all := strings.Join(res.Stmts, "\n")
	allFK := strings.Join(append(append([]string{}, res.FKDropStmts...), res.FKAddStmts...), "\n")
	assert.Contains(t, all, "ALTER TABLE shop.users RENAME TO shop.members;")
	assert.NotContains(t, allFK, "DROP FOREIGN KEY", "FK on referencing table must not be dropped after rename")
	assert.NotContains(t, allFK, "ADD CONSTRAINT", "FK on referencing table must not be re-added after rename")
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
