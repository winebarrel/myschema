package parser_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/winebarrel/myschema/parser"
)

func TestParseCreateTableBasics(t *testing.T) {
	sql := `
CREATE TABLE users (
    id BIGINT NOT NULL AUTO_INCREMENT,
    email VARCHAR(255) NOT NULL,
    name VARCHAR(64),
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (id),
    UNIQUE KEY users_email_key (email)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
`
	r, err := parser.ParseSQL(sql, "app")
	require.NoError(t, err)

	tbl, ok := r.Tables.GetOk("app.users")
	require.True(t, ok, "table app.users should be parsed")
	assert.Equal(t, "users", tbl.Name)
	assert.Equal(t, "app", tbl.Database)

	// Columns
	id, _ := tbl.Columns.GetOk("id")
	require.NotNil(t, id)
	assert.Equal(t, "bigint", id.TypeName)
	assert.True(t, id.NotNull)
	assert.True(t, id.AutoIncrement)

	email, _ := tbl.Columns.GetOk("email")
	require.NotNil(t, email)
	assert.Equal(t, "varchar(255)", email.TypeName)

	// PRIMARY KEY → constraint + mirror index
	pk, ok := tbl.Constraints.GetOk("PRIMARY")
	require.True(t, ok)
	assert.Equal(t, []string{"id"}, pk.Columns)

	idx, ok := tbl.Indexes.GetOk("users_email_key")
	require.True(t, ok)
	assert.Equal(t, "UNIQUE", string(idx.KeyType))

	// Engine / charset
	require.NotNil(t, tbl.Engine)
	assert.Equal(t, "InnoDB", *tbl.Engine)
}

func TestParseForeignKey(t *testing.T) {
	sql := `
CREATE TABLE users (id BIGINT NOT NULL, PRIMARY KEY (id));
CREATE TABLE posts (
    id BIGINT NOT NULL,
    user_id BIGINT NOT NULL,
    PRIMARY KEY (id),
    CONSTRAINT fk_posts_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);
`
	r, err := parser.ParseSQL(sql, "app")
	require.NoError(t, err)
	posts, ok := r.Tables.GetOk("app.posts")
	require.True(t, ok)
	fk, ok := posts.ForeignKeys.GetOk("fk_posts_user")
	require.True(t, ok)
	assert.Equal(t, []string{"user_id"}, fk.Columns)
	assert.Equal(t, "users", fk.RefTable)
	assert.Equal(t, "app", fk.RefDB)
	assert.Equal(t, "CASCADE", fk.OnDelete)
}

func TestParseCreateIndex(t *testing.T) {
	sql := `
CREATE TABLE users (id BIGINT NOT NULL, name VARCHAR(64), PRIMARY KEY (id));
CREATE INDEX idx_users_name ON users (name);
`
	r, err := parser.ParseSQL(sql, "app")
	require.NoError(t, err)
	users, _ := r.Tables.GetOk("app.users")
	idx, ok := users.Indexes.GetOk("idx_users_name")
	require.True(t, ok)
	require.Len(t, idx.Parts, 1)
	assert.Equal(t, "name", idx.Parts[0].Column)
}
