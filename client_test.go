package myschema_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/winebarrel/myschema"
)

func TestClient_DSNConfig_PasswordOverride(t *testing.T) {
	// MYSCHEMA_PASSWORD overrides whatever password the DSN already has.
	c := myschema.NewClient(&myschema.Options{
		DSN:      "root:from_dsn@tcp(127.0.0.1:3306)/mydb",
		Password: "from_env",
	})
	dbName, password, err := myschema.DSNConfigFor(c)
	require.NoError(t, err)
	assert.Equal(t, "mydb", dbName)
	assert.Equal(t, "from_env", password, "MYSCHEMA_PASSWORD should override DSN password")
}

func TestClient_DSNConfig_MalformedDSNError(t *testing.T) {
	c := myschema.NewClient(&myschema.Options{DSN: "this is not a valid DSN"})
	_, _, err := myschema.DSNConfigFor(c)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse DSN")
}

func TestClient_DSNConfig_MissingDBError(t *testing.T) {
	// DSN parses but doesn't carry a database — myschema needs one
	// because every operation is scoped to a single schema.
	c := myschema.NewClient(&myschema.Options{DSN: "root@tcp(127.0.0.1:3306)/"})
	_, _, err := myschema.DSNConfigFor(c)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must include a database name")
}

func TestClient_Database(t *testing.T) {
	c := myschema.NewClient(&myschema.Options{
		DSN: "root@tcp(127.0.0.1:3306)/mydb",
	})
	got, err := c.Database()
	require.NoError(t, err)
	assert.Equal(t, "mydb", got)
}

func TestClient_Database_BadDSNError(t *testing.T) {
	c := myschema.NewClient(&myschema.Options{DSN: "garbage"})
	_, err := c.Database()
	require.Error(t, err)
}
