package model_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/winebarrel/myschema/model"
)

func TestIndexSQL(t *testing.T) {
	t.Run("plain BTREE", func(t *testing.T) {
		idx := &model.Index{
			Name:     "idx_email",
			Database: "shop", Table: "users",
			Parts:     []model.IndexPart{{Column: "email"}},
			IndexType: "BTREE",
		}
		assert.Equal(t,
			"CREATE INDEX idx_email ON shop.users (email) USING BTREE;",
			idx.SQL())
	})

	t.Run("UNIQUE multi-column", func(t *testing.T) {
		idx := &model.Index{
			Name:     "uq_pair",
			Database: "shop", Table: "users",
			KeyType: model.IndexUnique,
			Parts: []model.IndexPart{
				{Column: "email"},
				{Column: "tenant_id"},
			},
		}
		assert.Equal(t,
			"CREATE UNIQUE INDEX uq_pair ON shop.users (email, tenant_id);",
			idx.SQL())
	})

	t.Run("FULLTEXT", func(t *testing.T) {
		idx := &model.Index{
			Name:     "ft",
			Database: "shop", Table: "docs",
			KeyType: model.IndexFulltext,
			Parts:   []model.IndexPart{{Column: "body"}},
		}
		assert.Contains(t, idx.SQL(), "CREATE FULLTEXT INDEX")
	})

	t.Run("prefix length", func(t *testing.T) {
		idx := &model.Index{
			Name:     "i",
			Database: "shop", Table: "users",
			Parts: []model.IndexPart{{Column: "name", Length: 20}},
		}
		assert.Contains(t, idx.SQL(), "(name(20))")
	})

	t.Run("DESC", func(t *testing.T) {
		idx := &model.Index{
			Name:     "i",
			Database: "shop", Table: "users",
			Parts: []model.IndexPart{{Column: "id", Desc: true}},
		}
		assert.Contains(t, idx.SQL(), "id DESC")
	})

	t.Run("INVISIBLE", func(t *testing.T) {
		idx := &model.Index{
			Name:     "i",
			Database: "shop", Table: "users",
			Parts:     []model.IndexPart{{Column: "id"}},
			Invisible: true,
		}
		assert.Contains(t, idx.SQL(), "INVISIBLE")
	})

	t.Run("expression part", func(t *testing.T) {
		idx := &model.Index{
			Name:     "i",
			Database: "shop", Table: "users",
			Parts: []model.IndexPart{{Expr: "lower(email)"}},
		}
		assert.Contains(t, idx.SQL(), "((lower(email)))")
	})

	t.Run("comment", func(t *testing.T) {
		c := "primary lookup"
		idx := &model.Index{
			Name:     "i",
			Database: "shop", Table: "users",
			Parts:   []model.IndexPart{{Column: "id"}},
			Comment: &c,
		}
		assert.Contains(t, idx.SQL(), "COMMENT 'primary lookup'")
	})
}

func TestIndexPartSQL(t *testing.T) {
	tests := []struct {
		name string
		part model.IndexPart
		want string
	}{
		{"plain column", model.IndexPart{Column: "email"}, "email"},
		{"prefix length", model.IndexPart{Column: "name", Length: 20}, "name(20)"},
		{"desc", model.IndexPart{Column: "id", Desc: true}, "id DESC"},
		{"expression", model.IndexPart{Expr: "lower(email)"}, "(lower(email))"},
		{"expression desc", model.IndexPart{Expr: "x+1", Desc: true}, "(x+1) DESC"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.part.SQL())
		})
	}
}
