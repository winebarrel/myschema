package model_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/winebarrel/myschema/model"
	"github.com/winebarrel/orderedmap"
)

// emptyTable is a minimal Table with no objects, used as a starting
// point for the renderer tests. ordered maps are non-nil so callers can
// just .Set on the fields they care about.
func emptyTable(db, name string) *model.Table {
	return &model.Table{
		Database:    db,
		Name:        name,
		Columns:     orderedmap.New[string, *model.Column](),
		Constraints: orderedmap.New[string, *model.Constraint](),
		ForeignKeys: orderedmap.New[string, *model.ForeignKey](),
		Indexes:     orderedmap.New[string, *model.Index](),
	}
}

func TestTableSQLBasic(t *testing.T) {
	tbl := emptyTable("shop", "users")
	tbl.Columns.Set("id", &model.Column{
		Name: "id", TypeName: "bigint", NotNull: true, AutoIncrement: true,
	})
	tbl.Constraints.Set("PRIMARY", &model.Constraint{
		Name: "PRIMARY", Type: model.PrimaryKeyConstraint,
		Definition: "(id)", Columns: []string{"id"},
	})

	sql := tbl.SQL()
	assert.Contains(t, sql, "CREATE TABLE users (")
	assert.Contains(t, sql, "id bigint NOT NULL AUTO_INCREMENT")
	assert.Contains(t, sql, "PRIMARY KEY (id)")
	assert.True(t, strings.HasSuffix(sql, ";"), "should end with ;")
}

func TestTableSQLWithCheckConstraint(t *testing.T) {
	tbl := emptyTable("shop", "products")
	tbl.Columns.Set("id", &model.Column{Name: "id", TypeName: "bigint", NotNull: true})
	tbl.Columns.Set("price", &model.Column{Name: "price", TypeName: "int"})
	tbl.Constraints.Set("PRIMARY", &model.Constraint{
		Name: "PRIMARY", Type: model.PrimaryKeyConstraint,
		Definition: "(id)", Columns: []string{"id"},
	})
	// con.Definition is already `CHECK (...)` per the parser/catalog
	// invariant — the renderer must not prepend another CHECK keyword.
	tbl.Constraints.Set("chk_price", &model.Constraint{
		Name: "chk_price", Type: model.CheckConstraint,
		Definition: "CHECK (price > 0)", Enforced: true,
	})

	sql := tbl.SQL()
	assert.Contains(t, sql, "CONSTRAINT chk_price CHECK (price > 0)")
	assert.NotContains(t, sql, "CHECK CHECK", "regression: don't double the CHECK keyword")
}

func TestTableSQLWithEngineCharsetCollationComment(t *testing.T) {
	engine, charset, coll, comment := "InnoDB", "utf8mb4", "utf8mb4_0900_ai_ci", "hello"
	tbl := emptyTable("shop", "t")
	tbl.Columns.Set("id", &model.Column{Name: "id", TypeName: "bigint", NotNull: true})
	tbl.Constraints.Set("PRIMARY", &model.Constraint{
		Name: "PRIMARY", Type: model.PrimaryKeyConstraint,
		Definition: "(id)", Columns: []string{"id"},
	})
	tbl.Engine, tbl.Charset, tbl.Collation, tbl.Comment = &engine, &charset, &coll, &comment

	sql := tbl.SQL()
	assert.Contains(t, sql, "ENGINE=InnoDB")
	assert.Contains(t, sql, "DEFAULT CHARSET=utf8mb4")
	assert.Contains(t, sql, "COLLATE=utf8mb4_0900_ai_ci")
	assert.Contains(t, sql, "COMMENT='hello'")
}

func TestTableIdxAndFkSQL(t *testing.T) {
	tbl := emptyTable("shop", "posts")
	tbl.Columns.Set("id", &model.Column{Name: "id", TypeName: "bigint", NotNull: true})
	tbl.Columns.Set("user_id", &model.Column{Name: "user_id", TypeName: "bigint", NotNull: true})
	// PRIMARY KEY mirror — emitted inline in CREATE TABLE, NOT via IdxSQL.
	tbl.Indexes.Set("PRIMARY", &model.Index{
		Name: "PRIMARY", Database: "shop", Table: "posts",
		Primary: true,
		Parts:   []model.IndexPart{{Column: "id"}},
	})
	// Secondary index — appears in IdxSQL.
	tbl.Indexes.Set("idx_user", &model.Index{
		Name: "idx_user", Database: "shop", Table: "posts",
		Parts: []model.IndexPart{{Column: "user_id"}},
	})
	tbl.ForeignKeys.Set("fk_user", &model.ForeignKey{
		Name: "fk_user", Database: "shop", Table: "posts",
		Columns: []string{"user_id"},
		RefDB:   "shop", RefTable: "users", RefCols: []string{"id"},
	})

	idxSQL := tbl.IdxSQL()
	assert.Contains(t, idxSQL, "CREATE INDEX idx_user ON ")
	assert.NotContains(t, idxSQL, "PRIMARY", "PRIMARY KEY index excluded from IdxSQL")

	fkSQL := tbl.FkSQL()
	assert.Contains(t, fkSQL, "ADD CONSTRAINT fk_user FOREIGN KEY (user_id) REFERENCES users(id)")
}

func TestTableToSQLCombined(t *testing.T) {
	tbl := emptyTable("shop", "posts")
	tbl.Columns.Set("id", &model.Column{Name: "id", TypeName: "bigint", NotNull: true})
	tbl.Columns.Set("user_id", &model.Column{Name: "user_id", TypeName: "bigint", NotNull: true})
	tbl.Constraints.Set("PRIMARY", &model.Constraint{
		Name: "PRIMARY", Type: model.PrimaryKeyConstraint,
		Definition: "(id)", Columns: []string{"id"},
	})
	tbl.Indexes.Set("idx_user", &model.Index{
		Name: "idx_user", Database: "shop", Table: "posts",
		Parts: []model.IndexPart{{Column: "user_id"}},
	})
	tbl.ForeignKeys.Set("fk_user", &model.ForeignKey{
		Name: "fk_user", Database: "shop", Table: "posts",
		Columns: []string{"user_id"},
		RefDB:   "shop", RefTable: "users", RefCols: []string{"id"},
	})

	out := model.TableToSQL(tbl)
	assert.Contains(t, out, "-- posts", "leading comment marker")
	assert.Contains(t, out, "CREATE TABLE posts (")
	assert.Contains(t, out, "CREATE INDEX idx_user ON ")
	assert.Contains(t, out, "ADD CONSTRAINT fk_user FOREIGN KEY")
}

func TestTablesToSQLOrderingAndSeparators(t *testing.T) {
	tables := orderedmap.New[string, *model.Table]()
	a := emptyTable("shop", "a")
	a.Columns.Set("id", &model.Column{Name: "id", TypeName: "int", NotNull: true})
	a.Constraints.Set("PRIMARY", &model.Constraint{
		Name: "PRIMARY", Type: model.PrimaryKeyConstraint, Definition: "(id)", Columns: []string{"id"},
	})
	b := emptyTable("shop", "b")
	b.Columns.Set("id", &model.Column{Name: "id", TypeName: "int", NotNull: true})
	b.Constraints.Set("PRIMARY", &model.Constraint{
		Name: "PRIMARY", Type: model.PrimaryKeyConstraint, Definition: "(id)", Columns: []string{"id"},
	})
	tables.Set("shop.a", a)
	tables.Set("shop.b", b)

	out := model.TablesToSQL(tables)
	// Tables are emitted unqualified (`-- a`, `CREATE TABLE a`); pin
	// the order on the leading comment markers so the assertion
	// doesn't false-match a substring inside another table's body.
	posA := strings.Index(out, "-- a")
	posB := strings.Index(out, "-- b")
	require.True(t, posA >= 0 && posB >= 0)
	assert.Less(t, posA, posB, "insertion order preserved")
	assert.Contains(t, out, "\n\n", "tables separated by blank line")
}

func TestColumnDefSQLAttributes(t *testing.T) {
	def := "CURRENT_TIMESTAMP"
	cs, coll := "utf8mb4", "utf8mb4_0900_ai_ci"
	gen := "a + b"
	comment := "hello"

	c := &model.Column{
		Name: "x", TypeName: "varchar(64)",
		NotNull: true,
		Default: &def, OnUpdate: &def,
		AutoIncrement: false,
		CharacterSet:  &cs, Collation: &coll,
		Generated: &gen, Stored: true,
		Comment: &comment,
	}
	out := model.ColumnDefSQL(c)

	for _, expect := range []string{
		"x varchar(64)",
		"CHARACTER SET utf8mb4",
		"COLLATE utf8mb4_0900_ai_ci",
		"GENERATED ALWAYS AS (a + b) STORED",
		"NOT NULL",
		"DEFAULT CURRENT_TIMESTAMP",
		"ON UPDATE CURRENT_TIMESTAMP",
		"COMMENT 'hello'",
	} {
		assert.Contains(t, out, expect, "ColumnDefSQL must emit %q", expect)
	}
}

func TestColumnDefSQLAutoIncrementAndQuotedComment(t *testing.T) {
	c := &model.Column{Name: "id", TypeName: "bigint", NotNull: true, AutoIncrement: true}
	out := model.ColumnDefSQL(c)
	assert.Contains(t, out, "AUTO_INCREMENT")

	with := "it's a comment"
	c2 := &model.Column{Name: "x", TypeName: "varchar(64)", Comment: &with}
	out2 := model.ColumnDefSQL(c2)
	assert.Contains(t, out2, "COMMENT 'it''s a comment'", "embedded quote escaped")
}

// TestColumnDefSQLVirtualGenerated covers the Stored=false branch.
func TestColumnDefSQLVirtualGenerated(t *testing.T) {
	expr := "a + b"
	c := &model.Column{Name: "x", TypeName: "int", Generated: &expr, Stored: false}
	out := model.ColumnDefSQL(c)
	assert.Contains(t, out, "GENERATED ALWAYS AS (a + b) VIRTUAL")
}
