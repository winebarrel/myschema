package model_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/winebarrel/myschema/model"
)

func TestForeignKeySQL(t *testing.T) {
	t.Run("basic", func(t *testing.T) {
		fk := &model.ForeignKey{
			Name:     "fk_posts_user",
			Database: "shop", Table: "posts",
			Columns: []string{"user_id"},
			RefDB:   "shop", RefTable: "users",
			RefCols: []string{"id"},
		}
		assert.Equal(t,
			"ALTER TABLE posts ADD CONSTRAINT fk_posts_user FOREIGN KEY (user_id) REFERENCES users(id);",
			fk.SQL())
	})

	t.Run("with referential actions", func(t *testing.T) {
		fk := &model.ForeignKey{
			Name:     "fk",
			Database: "shop", Table: "posts",
			Columns: []string{"user_id"},
			RefDB:   "shop", RefTable: "users",
			RefCols:  []string{"id"},
			OnDelete: "CASCADE",
			OnUpdate: "SET NULL",
		}
		s := fk.SQL()
		assert.Contains(t, s, "ON DELETE CASCADE")
		assert.Contains(t, s, "ON UPDATE SET NULL")
	})

	t.Run("multi-column", func(t *testing.T) {
		fk := &model.ForeignKey{
			Name:     "fk",
			Database: "shop", Table: "rel",
			Columns: []string{"a", "b"},
			RefDB:   "shop", RefTable: "src",
			RefCols: []string{"x", "y"},
		}
		s := fk.SQL()
		assert.Contains(t, s, "FOREIGN KEY (a, b)")
		assert.Contains(t, s, "REFERENCES src(x, y)")
	})

	t.Run("MATCH FULL", func(t *testing.T) {
		fk := &model.ForeignKey{
			Name:     "fk",
			Database: "shop", Table: "posts",
			Columns: []string{"user_id"},
			RefDB:   "shop", RefTable: "users",
			RefCols:   []string{"id"},
			MatchType: "FULL",
		}
		assert.Contains(t, fk.SQL(), "MATCH FULL")
	})

	t.Run("cross-database REFERENCES keeps the qualifier", func(t *testing.T) {
		// myschema doesn't fully manage cross-database FKs (see
		// TODO.md), but emission must not silently re-target one
		// the user spelled out by hand: dropping the `other_db.`
		// prefix would point the FK at a same-named table in the
		// current DB.
		fk := &model.ForeignKey{
			Name:     "fk_external",
			Database: "shop", Table: "posts",
			Columns: []string{"user_id"},
			RefDB:   "other_db", RefTable: "users",
			RefCols: []string{"id"},
		}
		assert.Contains(t, fk.SQL(), "REFERENCES other_db.users(id)")
	})
}
