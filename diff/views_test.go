package diff

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/winebarrel/myschema/model"
	"github.com/winebarrel/orderedmap"
)

// view is a tiny constructor that mirrors what the parser would produce
// for a CREATE VIEW. Tests only need Database / Name / Definition.
func view(t *testing.T, db, name, def string) *model.View {
	t.Helper()
	return &model.View{Database: db, Name: name, Definition: def}
}

func TestTopoSortViewsLinear(t *testing.T) {
	// b depends on a → output is [a, b].
	views := orderedmap.New[string, *model.View]()
	a := view(t, "app", "a", "SELECT 1")
	b := view(t, "app", "b", "SELECT * FROM a")
	views.Set(a.FQVN(), a)
	views.Set(b.FQVN(), b)

	sorted, err := topoSortViews(views, "app")
	require.NoError(t, err)
	require.Len(t, sorted, 2)
	assert.Equal(t, "a", sorted[0].Name)
	assert.Equal(t, "b", sorted[1].Name)
}

func TestTopoSortViewsDeterministicTies(t *testing.T) {
	// Two independent views — alphabetical FQVN tie-break.
	views := orderedmap.New[string, *model.View]()
	z := view(t, "app", "z", "SELECT 1")
	a := view(t, "app", "a", "SELECT 1")
	views.Set(z.FQVN(), z)
	views.Set(a.FQVN(), a)

	sorted, err := topoSortViews(views, "app")
	require.NoError(t, err)
	require.Len(t, sorted, 2)
	assert.Equal(t, "a", sorted[0].Name)
	assert.Equal(t, "z", sorted[1].Name)
}

func TestTopoSortViewsCycle(t *testing.T) {
	// a → b → a is a cycle.
	views := orderedmap.New[string, *model.View]()
	a := view(t, "app", "a", "SELECT * FROM b")
	b := view(t, "app", "b", "SELECT * FROM a")
	views.Set(a.FQVN(), a)
	views.Set(b.FQVN(), b)

	_, err := topoSortViews(views, "app")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "circular")
}

func TestTopoSortViewsIgnoresUnknownRefs(t *testing.T) {
	// Reference to a non-view (e.g. a base table) should not become a dep.
	views := orderedmap.New[string, *model.View]()
	a := view(t, "app", "a", "SELECT * FROM users") // users is a table, not a view
	views.Set(a.FQVN(), a)

	sorted, err := topoSortViews(views, "app")
	require.NoError(t, err)
	require.Len(t, sorted, 1)
	assert.Equal(t, "a", sorted[0].Name)
}
