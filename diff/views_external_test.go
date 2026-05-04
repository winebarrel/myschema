package diff_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/winebarrel/myschema/diff"
	"github.com/winebarrel/myschema/model"
	"github.com/winebarrel/orderedmap"
)

func newView(db, name, def string) *model.View {
	return &model.View{Database: db, Name: name, Definition: def}
}

func TestDiffViews_CreateNew(t *testing.T) {
	current := orderedmap.New[string, *model.View]()
	desired := orderedmap.New[string, *model.View]()
	v := newView("app", "active_users", "SELECT id FROM users")
	desired.Set(v.FQVN(), v)

	r, err := diff.DiffViews(current, desired, "app", nil)
	require.NoError(t, err)
	require.Len(t, r.CreateStmts, 1)
	assert.Contains(t, r.CreateStmts[0], "CREATE OR REPLACE VIEW")
}

func TestDiffViews_DropAllowed(t *testing.T) {
	current := orderedmap.New[string, *model.View]()
	v := newView("app", "old_v", "SELECT 1")
	current.Set(v.FQVN(), v)
	desired := orderedmap.New[string, *model.View]()

	r, err := diff.DiffViews(current, desired, "app", &diff.AllowList{Kinds: map[string]bool{"view": true}})
	require.NoError(t, err)
	require.Len(t, r.DropStmts, 1)
	assert.Contains(t, r.DropStmts[0], "DROP VIEW")
	assert.Empty(t, r.DisallowedDropStmts)
}

func TestDiffViews_DropDisallowedSurfacesAsCommentedSkip(t *testing.T) {
	current := orderedmap.New[string, *model.View]()
	v := newView("app", "old_v", "SELECT 1")
	current.Set(v.FQVN(), v)
	desired := orderedmap.New[string, *model.View]()

	r, err := diff.DiffViews(current, desired, "app", &diff.AllowList{Kinds: map[string]bool{}})
	require.NoError(t, err)
	assert.Empty(t, r.DropStmts, "drop must be suppressed when not allowed")
	require.Len(t, r.DisallowedDropStmts, 1)
	assert.Contains(t, r.DisallowedDropStmts[0], "-- skipped: ")
	assert.Contains(t, r.DisallowedDropStmts[0], "DROP VIEW")
}

func TestDiffViews_SameDefinitionSkipped(t *testing.T) {
	current := orderedmap.New[string, *model.View]()
	desired := orderedmap.New[string, *model.View]()
	a := newView("app", "v", "SELECT id FROM users")
	b := newView("app", "v", "SELECT `app`.`users`.`id` AS `id` FROM `app`.`users`")
	current.Set(a.FQVN(), a)
	desired.Set(b.FQVN(), b)

	r, err := diff.DiffViews(current, desired, "app", nil)
	require.NoError(t, err)
	assert.Empty(t, r.CreateStmts, "normalised-equal definitions must not re-emit CREATE OR REPLACE")
	assert.Empty(t, r.DropStmts)
}

// Regression for the "DiffViews ignores CheckOption" gap: pre-fix,
// viewDefEqual only inspected Definition, so adding / removing
// `WITH … CHECK OPTION` would silently compare equal and the live
// view would never be replaced. Cols changes have the same shape of
// gap but can't be fixed at the diff layer alone (catalog/views.go
// reads only `information_schema.VIEWS`, which doesn't expose the
// user-supplied alias list); see TODO.md for the catalog-side
// follow-up.

func TestDiffViews_CheckOptionChangeFiresDiff(t *testing.T) {
	current := orderedmap.New[string, *model.View]()
	desired := orderedmap.New[string, *model.View]()
	cv := &model.View{Database: "app", Name: "v", Definition: "select id from users", CheckOption: "NONE"}
	dv := &model.View{Database: "app", Name: "v", Definition: "select id from users", CheckOption: "LOCAL"}
	current.Set(cv.FQVN(), cv)
	desired.Set(dv.FQVN(), dv)

	r, err := diff.DiffViews(current, desired, "app", nil)
	require.NoError(t, err)
	require.Len(t, r.CreateStmts, 1, "WITH LOCAL CHECK OPTION change must fire a CREATE OR REPLACE")
	assert.Contains(t, r.CreateStmts[0], "WITH LOCAL CHECK OPTION")
}

func TestDiffViews_CheckOptionEmptyEquivalentToNone(t *testing.T) {
	// `""` and `"NONE"` both mean "no WITH … CHECK OPTION clause" —
	// (*View).CreateSQL() suppresses the clause for both. viewEqual
	// must treat them as equal, otherwise a hand-built model.View{}
	// (zero-value CheckOption) compared to a parser/catalog-built
	// view (CheckOption="NONE") would emit a spurious CREATE OR REPLACE.
	current := orderedmap.New[string, *model.View]()
	desired := orderedmap.New[string, *model.View]()
	cv := &model.View{Database: "app", Name: "v", Definition: "select id from users", CheckOption: "NONE"}
	dv := &model.View{Database: "app", Name: "v", Definition: "select id from users"} // empty CheckOption
	current.Set(cv.FQVN(), cv)
	desired.Set(dv.FQVN(), dv)

	r, err := diff.DiffViews(current, desired, "app", nil)
	require.NoError(t, err)
	assert.Empty(t, r.CreateStmts, `"" must compare equal to "NONE"`)
}

func TestDiffViews_AllFieldsEqualSkipped(t *testing.T) {
	// Sanity: same Definition + same CheckOption must still skip the
	// CREATE OR REPLACE (the fix mustn't introduce false positives for
	// genuinely-equal views).
	current := orderedmap.New[string, *model.View]()
	desired := orderedmap.New[string, *model.View]()
	cv := &model.View{Database: "app", Name: "v", Definition: "select id from users", CheckOption: "LOCAL"}
	dv := &model.View{Database: "app", Name: "v", Definition: "select id from users", CheckOption: "LOCAL"}
	current.Set(cv.FQVN(), cv)
	desired.Set(dv.FQVN(), dv)

	r, err := diff.DiffViews(current, desired, "app", nil)
	require.NoError(t, err)
	assert.Empty(t, r.CreateStmts)
}

func TestDiffViews_DesiredCircularSurfacesError(t *testing.T) {
	// a → b → a in desired forces topoSortViews to surface a circular
	// dependency, which DiffViews wraps as "topo-sort desired views: %w".
	current := orderedmap.New[string, *model.View]()
	desired := orderedmap.New[string, *model.View]()
	a := newView("app", "a", "SELECT * FROM b")
	b := newView("app", "b", "SELECT * FROM a")
	desired.Set(a.FQVN(), a)
	desired.Set(b.FQVN(), b)

	_, err := diff.DiffViews(current, desired, "app", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "desired views")
}

func TestDiffViews_CurrentCircularSurfacesError(t *testing.T) {
	// Same shape but on the current side — pins the second topoSortViews
	// call's error wrap ("topo-sort current views: %w").
	current := orderedmap.New[string, *model.View]()
	a := newView("app", "a", "SELECT * FROM b")
	b := newView("app", "b", "SELECT * FROM a")
	current.Set(a.FQVN(), a)
	current.Set(b.FQVN(), b)
	desired := orderedmap.New[string, *model.View]()

	_, err := diff.DiffViews(current, desired, "app", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "current views")
}

func TestTopoSortViews_SelfReferenceSkipped(t *testing.T) {
	// A view that references itself is allowed (the self-edge is dropped
	// at the `r == k` guard rather than counted as a dependency, which
	// would otherwise inflate indeg and fail Kahn's algorithm).
	views := orderedmap.New[string, *model.View]()
	v := newView("app", "v", "SELECT * FROM v") // selects from itself
	views.Set(v.FQVN(), v)

	sorted, err := diff.TopoSortViews(views, "app")
	require.NoError(t, err)
	require.Len(t, sorted, 1)
	assert.Equal(t, "v", sorted[0].Name)
}

func TestTopoSortViews_ParseErrorWrapped(t *testing.T) {
	// A view whose Definition doesn't parse surfaces as a wrapped
	// `view <FQVN>: <parse error>` so the operator can find the
	// offending object.
	views := orderedmap.New[string, *model.View]()
	v := newView("app", "broken", "this is not valid SELECT")
	views.Set(v.FQVN(), v)

	_, err := diff.TopoSortViews(views, "app")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "view app.broken")
}

func TestViewDefEqual(t *testing.T) {
	t.Run("byte-equal short circuit", func(t *testing.T) {
		eq, err := diff.ViewDefEqual("SELECT 1", "SELECT 1", "app")
		require.NoError(t, err)
		assert.True(t, eq)
	})
	t.Run("normalised match", func(t *testing.T) {
		eq, err := diff.ViewDefEqual(
			"SELECT id FROM users",
			"SELECT `app`.`users`.`id` AS `id` FROM `app`.`users`",
			"app",
		)
		require.NoError(t, err)
		assert.True(t, eq)
	})
	t.Run("unparseable left side falls back to byte-equality", func(t *testing.T) {
		eq, err := diff.ViewDefEqual("garbage", "SELECT 1", "app")
		require.NoError(t, err)
		assert.False(t, eq)
	})
	t.Run("unparseable right side falls back to byte-equality", func(t *testing.T) {
		eq, err := diff.ViewDefEqual("SELECT 1", "garbage", "app")
		require.NoError(t, err)
		assert.False(t, eq)
	})
}
