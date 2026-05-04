package model_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/winebarrel/myschema/model"
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
