package diff_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/winebarrel/myschema/diff"
	"github.com/winebarrel/myschema/model"
)

func sptr(s string) *string { return &s }

func TestPtrEq(t *testing.T) {
	assert.True(t, diff.PtrEq(nil, nil), "both nil")
	assert.False(t, diff.PtrEq(sptr("x"), nil), "left set, right nil")
	assert.False(t, diff.PtrEq(nil, sptr("x")), "left nil, right set")
	assert.True(t, diff.PtrEq(sptr("x"), sptr("x")), "both set, equal")
	assert.False(t, diff.PtrEq(sptr("x"), sptr("y")), "both set, different")
}

func TestSliceEq(t *testing.T) {
	assert.True(t, diff.SliceEqString([]string{}, []string{}), "both empty")
	assert.True(t, diff.SliceEqString([]string{"a", "b"}, []string{"a", "b"}), "equal")
	assert.False(t, diff.SliceEqString([]string{"a"}, []string{"a", "b"}), "different length")
	assert.False(t, diff.SliceEqString([]string{"a", "b"}, []string{"a", "c"}), "differs at index 1")
	assert.True(t, diff.SliceEqInt([]int{1, 2, 3}, []int{1, 2, 3}), "int slices")
}

func TestLooseEqual(t *testing.T) {
	tests := []struct {
		name string
		a, b string
		want bool
	}{
		{"identical", "(a, b)", "(a, b)", true},
		{"casing", "(A, B)", "(a, b)", true},
		{"spacing", "(a, b)", "(a,b)", true},
		{"backticks", "(`a`, `b`)", "(a, b)", true},
		{"differ", "(a, b)", "(a, c)", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, diff.LooseEqual(tt.a, tt.b))
		})
	}
}

func TestNormalizeIndexType(t *testing.T) {
	assert.Equal(t, "", diff.NormalizeIndexTyp(""), "empty stays empty")
	assert.Equal(t, "", diff.NormalizeIndexTyp("BTREE"), "BTREE collapses to empty (InnoDB default)")
	assert.Equal(t, "", diff.NormalizeIndexTyp("btree"), "lowercase btree also collapses")
	assert.Equal(t, "HASH", diff.NormalizeIndexTyp("HASH"), "HASH preserved")
	assert.Equal(t, "FULLTEXT", diff.NormalizeIndexTyp("fulltext"), "uppercased")
	assert.Equal(t, "SPATIAL", diff.NormalizeIndexTyp("SPATIAL"), "SPATIAL preserved")
}

func TestColumnEqual(t *testing.T) {
	base := func() *model.Column {
		return &model.Column{
			Name:     "c",
			TypeName: "int",
			NotNull:  true,
		}
	}

	t.Run("identical", func(t *testing.T) {
		assert.True(t, diff.ColumnEqual(base(), base()))
	})
	t.Run("type differs", func(t *testing.T) {
		a, b := base(), base()
		b.TypeName = "bigint"
		assert.False(t, diff.ColumnEqual(a, b))
	})
	t.Run("not-null differs", func(t *testing.T) {
		a, b := base(), base()
		b.NotNull = false
		assert.False(t, diff.ColumnEqual(a, b))
	})
	t.Run("default differs", func(t *testing.T) {
		a, b := base(), base()
		a.Default = sptr("0")
		b.Default = sptr("1")
		assert.False(t, diff.ColumnEqual(a, b))
	})
	t.Run("default canonicalised match", func(t *testing.T) {
		a, b := base(), base()
		a.Default = sptr("CURRENT_TIMESTAMP")
		b.Default = sptr("current_timestamp()")
		assert.True(t, diff.ColumnEqual(a, b))
	})
	t.Run("on-update differs", func(t *testing.T) {
		a, b := base(), base()
		a.OnUpdate = sptr("CURRENT_TIMESTAMP")
		assert.False(t, diff.ColumnEqual(a, b))
	})
	t.Run("auto-increment differs", func(t *testing.T) {
		a, b := base(), base()
		b.AutoIncrement = true
		assert.False(t, diff.ColumnEqual(a, b))
	})
	t.Run("generated expr differs", func(t *testing.T) {
		a, b := base(), base()
		a.Generated = sptr("a + 1")
		b.Generated = sptr("a + 2")
		assert.False(t, diff.ColumnEqual(a, b))
	})
	t.Run("stored differs", func(t *testing.T) {
		a, b := base(), base()
		a.Generated = sptr("a + 1")
		b.Generated = sptr("a + 1")
		b.Stored = true
		assert.False(t, diff.ColumnEqual(a, b))
	})
	t.Run("comment differs", func(t *testing.T) {
		a, b := base(), base()
		a.Comment = sptr("hi")
		assert.False(t, diff.ColumnEqual(a, b))
	})
	t.Run("character set differs", func(t *testing.T) {
		a, b := base(), base()
		a.CharacterSet = sptr("utf8mb4")
		assert.False(t, diff.ColumnEqual(a, b))
	})
	t.Run("collation differs", func(t *testing.T) {
		a, b := base(), base()
		a.Collation = sptr("utf8mb4_bin")
		assert.False(t, diff.ColumnEqual(a, b))
	})
}

func TestConstraintEqual(t *testing.T) {
	t.Run("type differs (PK vs CHECK)", func(t *testing.T) {
		a := &model.Constraint{Type: model.PrimaryKeyConstraint, Definition: "(id)"}
		b := &model.Constraint{Type: model.CheckConstraint, Definition: "CHECK (id > 0)"}
		assert.False(t, diff.ConstraintEqual(a, b))
	})
	t.Run("CHECK identical", func(t *testing.T) {
		a := &model.Constraint{Type: model.CheckConstraint, Definition: "CHECK (id > 0)", Enforced: true}
		b := &model.Constraint{Type: model.CheckConstraint, Definition: "CHECK (id > 0)", Enforced: true}
		assert.True(t, diff.ConstraintEqual(a, b))
	})
	t.Run("CHECK enforcement differs", func(t *testing.T) {
		a := &model.Constraint{Type: model.CheckConstraint, Definition: "CHECK (id > 0)", Enforced: true}
		b := &model.Constraint{Type: model.CheckConstraint, Definition: "CHECK (id > 0)", Enforced: false}
		assert.False(t, diff.ConstraintEqual(a, b))
	})
	t.Run("CHECK expression differs", func(t *testing.T) {
		a := &model.Constraint{Type: model.CheckConstraint, Definition: "CHECK (id > 0)", Enforced: true}
		b := &model.Constraint{Type: model.CheckConstraint, Definition: "CHECK (id > 1)", Enforced: true}
		assert.False(t, diff.ConstraintEqual(a, b))
	})
	t.Run("CHECK expression equal after canonicalisation", func(t *testing.T) {
		a := &model.Constraint{Type: model.CheckConstraint, Definition: "CHECK (`id` > 0)", Enforced: true}
		b := &model.Constraint{Type: model.CheckConstraint, Definition: "CHECK (id>0)", Enforced: true}
		assert.True(t, diff.ConstraintEqual(a, b))
	})
	t.Run("PK columns equal across casing/spacing/backticks", func(t *testing.T) {
		a := &model.Constraint{Type: model.PrimaryKeyConstraint, Definition: "(`id`, `n`)"}
		b := &model.Constraint{Type: model.PrimaryKeyConstraint, Definition: "(ID, N)"}
		assert.True(t, diff.ConstraintEqual(a, b))
	})
	t.Run("PK columns differ", func(t *testing.T) {
		a := &model.Constraint{Type: model.PrimaryKeyConstraint, Definition: "(id, n)"}
		b := &model.Constraint{Type: model.PrimaryKeyConstraint, Definition: "(id)"}
		assert.False(t, diff.ConstraintEqual(a, b))
	})
}

func TestIndexEqual(t *testing.T) {
	base := func() *model.Index {
		return &model.Index{
			Name:    "i_x",
			KeyType: "INDEX",
			Parts:   []model.IndexPart{{Column: "x"}},
		}
	}

	t.Run("identical", func(t *testing.T) {
		assert.True(t, diff.IndexEqual(base(), base()))
	})
	t.Run("key type differs", func(t *testing.T) {
		a, b := base(), base()
		b.KeyType = "UNIQUE"
		assert.False(t, diff.IndexEqual(a, b))
	})
	t.Run("part count differs", func(t *testing.T) {
		a, b := base(), base()
		b.Parts = append(b.Parts, model.IndexPart{Column: "y"})
		assert.False(t, diff.IndexEqual(a, b))
	})
	t.Run("part column differs", func(t *testing.T) {
		a, b := base(), base()
		b.Parts = []model.IndexPart{{Column: "y"}}
		assert.False(t, diff.IndexEqual(a, b))
	})
	t.Run("part length differs", func(t *testing.T) {
		a, b := base(), base()
		b.Parts = []model.IndexPart{{Column: "x", Length: 16}}
		assert.False(t, diff.IndexEqual(a, b))
	})
	t.Run("part DESC differs", func(t *testing.T) {
		a, b := base(), base()
		b.Parts = []model.IndexPart{{Column: "x", Desc: true}}
		assert.False(t, diff.IndexEqual(a, b))
	})
	t.Run("BTREE vs empty index type compare equal", func(t *testing.T) {
		a, b := base(), base()
		a.IndexType = "BTREE"
		b.IndexType = ""
		assert.True(t, diff.IndexEqual(a, b))
	})
	t.Run("HASH vs BTREE differ", func(t *testing.T) {
		a, b := base(), base()
		a.IndexType = "HASH"
		assert.False(t, diff.IndexEqual(a, b))
	})
	t.Run("invisible differs", func(t *testing.T) {
		a, b := base(), base()
		b.Invisible = true
		assert.False(t, diff.IndexEqual(a, b))
	})
}

func TestFKEqual(t *testing.T) {
	base := func() *model.ForeignKey {
		return &model.ForeignKey{
			Name:     "fk",
			Columns:  []string{"a"},
			RefDB:    "db",
			RefTable: "parent",
			RefCols:  []string{"id"},
		}
	}

	t.Run("identical", func(t *testing.T) {
		assert.True(t, diff.FKEqual(base(), base()))
	})
	t.Run("columns differ", func(t *testing.T) {
		a, b := base(), base()
		b.Columns = []string{"b"}
		assert.False(t, diff.FKEqual(a, b))
	})
	t.Run("ref cols differ", func(t *testing.T) {
		a, b := base(), base()
		b.RefCols = []string{"id2"}
		assert.False(t, diff.FKEqual(a, b))
	})
	t.Run("ref DB differs", func(t *testing.T) {
		a, b := base(), base()
		b.RefDB = "other"
		assert.False(t, diff.FKEqual(a, b))
	})
	t.Run("ref table differs", func(t *testing.T) {
		a, b := base(), base()
		b.RefTable = "other"
		assert.False(t, diff.FKEqual(a, b))
	})
	t.Run("ON DELETE differs", func(t *testing.T) {
		a, b := base(), base()
		b.OnDelete = "CASCADE"
		assert.False(t, diff.FKEqual(a, b))
	})
	t.Run("ON UPDATE differs", func(t *testing.T) {
		a, b := base(), base()
		b.OnUpdate = "SET NULL"
		assert.False(t, diff.FKEqual(a, b))
	})
	t.Run("MATCH type differs", func(t *testing.T) {
		a, b := base(), base()
		b.MatchType = "FULL"
		assert.False(t, diff.FKEqual(a, b))
	})
}

func TestAllPartsDropped(t *testing.T) {
	idx := func(parts ...model.IndexPart) *model.Index {
		return &model.Index{Parts: parts}
	}

	t.Run("empty parts", func(t *testing.T) {
		assert.False(t, diff.AllPartsDropped(idx(), nil))
	})
	t.Run("all column parts dropped", func(t *testing.T) {
		dropped := map[string]bool{"a": true, "b": true}
		assert.True(t, diff.AllPartsDropped(idx(model.IndexPart{Column: "a"}, model.IndexPart{Column: "b"}), dropped))
	})
	t.Run("one part not dropped", func(t *testing.T) {
		dropped := map[string]bool{"a": true}
		assert.False(t, diff.AllPartsDropped(idx(model.IndexPart{Column: "a"}, model.IndexPart{Column: "b"}), dropped))
	})
	t.Run("expression part disqualifies", func(t *testing.T) {
		dropped := map[string]bool{"a": true}
		// Expression parts have Column="" — even if every other column is dropped, return false.
		assert.False(t, diff.AllPartsDropped(idx(model.IndexPart{Column: "a"}, model.IndexPart{Column: ""}), dropped))
	})
}
