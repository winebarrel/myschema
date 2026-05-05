package model_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/winebarrel/myschema/model"
	"github.com/winebarrel/orderedmap"
)

func TestViewCreateSQL(t *testing.T) {
	t.Run("plain", func(t *testing.T) {
		v := &model.View{
			Database:    "shop",
			Name:        "active_users",
			Definition:  "select id from users",
			CheckOption: "NONE",
		}
		assert.Equal(t,
			"CREATE OR REPLACE VIEW active_users AS select id from users;",
			v.CreateSQL())
	})

	t.Run("with column list", func(t *testing.T) {
		v := &model.View{
			Database:   "shop",
			Name:       "v",
			Cols:       []string{"a", "b"},
			Definition: "select 1, 2",
		}
		got := v.CreateSQL()
		assert.Contains(t, got, "(a, b)")
		assert.Contains(t, got, "AS select 1, 2;")
	})

	t.Run("with WITH LOCAL CHECK OPTION", func(t *testing.T) {
		v := &model.View{
			Database:    "shop",
			Name:        "v",
			Definition:  "select 1",
			CheckOption: "LOCAL",
		}
		assert.Contains(t, v.CreateSQL(), "WITH LOCAL CHECK OPTION;")
	})

	t.Run("CheckOption NONE suppressed", func(t *testing.T) {
		v := &model.View{Database: "shop", Name: "v", Definition: "select 1", CheckOption: "NONE"}
		assert.NotContains(t, v.CreateSQL(), "CHECK OPTION")
	})
}

func TestViewDropSQL(t *testing.T) {
	v := &model.View{Database: "shop", Name: "v"}
	assert.Equal(t, "DROP VIEW v;", v.DropSQL())
}

func TestViewFQVN(t *testing.T) {
	v := &model.View{Database: "shop", Name: "v"}
	assert.Equal(t, "shop.v", v.FQVN())
}

func TestViewToSQL(t *testing.T) {
	v := &model.View{
		Database:    "shop",
		Name:        "active_users",
		Definition:  "select id from users",
		CheckOption: "NONE",
	}
	got := model.ViewToSQL(v)
	assert.Contains(t, got, "-- active_users\n", "leading comment marker")
	assert.Contains(t, got, "CREATE OR REPLACE VIEW active_users")
}

func TestViewsToSQL(t *testing.T) {
	views := orderedmap.New[string, *model.View]()
	a := &model.View{Database: "shop", Name: "a", Definition: "select 1", CheckOption: "NONE"}
	b := &model.View{Database: "shop", Name: "b", Definition: "select 2", CheckOption: "NONE"}
	views.Set(a.FQVN(), a)
	views.Set(b.FQVN(), b)

	out := model.ViewsToSQL(views)
	posA := strings.Index(out, "-- a")
	posB := strings.Index(out, "-- b")
	require.True(t, posA >= 0 && posB >= 0)
	assert.Less(t, posA, posB, "insertion order preserved")
	assert.Contains(t, out, "\n\n", "views separated by blank line")
}
