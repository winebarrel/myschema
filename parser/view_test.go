package parser_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/winebarrel/myschema/parser"
)

func TestViewReferences(t *testing.T) {
	tests := []struct {
		name      string
		def       string
		defaultDB string
		want      []string
	}{
		{
			name:      "single table",
			def:       "SELECT id FROM users",
			defaultDB: "app",
			want:      []string{"app.users"},
		},
		{
			name:      "qualified",
			def:       "SELECT id FROM other_db.users",
			defaultDB: "app",
			want:      []string{"other_db.users"},
		},
		{
			name:      "join",
			def:       "SELECT u.id FROM users u JOIN orders o ON u.id = o.user_id",
			defaultDB: "app",
			want:      []string{"app.users", "app.orders"},
		},
		{
			name:      "subquery",
			def:       "SELECT * FROM (SELECT id FROM inner_view) x",
			defaultDB: "app",
			want:      []string{"app.inner_view"},
		},
		{
			name:      "deduplicates",
			def:       "SELECT a.id FROM users a JOIN users b ON a.id = b.id",
			defaultDB: "app",
			want:      []string{"app.users"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parser.ViewReferences(tt.def, tt.defaultDB)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestNormalizeViewDefinition(t *testing.T) {
	tests := []struct {
		name      string
		def       string
		defaultDB string
		want      string
	}{
		{
			name: "empty stays empty",
			def:  "",
			want: "",
		},
		{
			name:      "lowercases keywords",
			def:       "SELECT id FROM users",
			defaultDB: "app",
			want:      "select id from users",
		},
		{
			name:      "strips db.table.col qualifiers from columns",
			def:       "SELECT `app`.`users`.`id` FROM `app`.`users`",
			defaultDB: "app",
			want:      "select id from users",
		},
		{
			name:      "strips AS alias (redundant on round-trip)",
			def:       "SELECT id AS id FROM users",
			defaultDB: "app",
			want:      "select id from users",
		},
		{
			// Regression: stripRedundantAliases used to drop *every*
			// non-empty alias, which silently equated `amount AS unit_price`
			// to `amount AS total_price`. A meaningful alias must survive
			// the normaliser so view-body changes that only rename a
			// column-alias still surface as a diff.
			name:      "preserves meaningful AS alias",
			def:       "SELECT amount AS unit_price FROM orders",
			defaultDB: "app",
			want:      "select amount as unit_price from orders",
		},
		{
			// Aliases attached to non-ColName expressions (function
			// calls, arithmetic, …) are always meaningful — there is no
			// "redundant" case to detect because the underlying expr has
			// no canonical name.
			name:      "preserves alias on function call",
			def:       "SELECT COUNT(*) AS total FROM orders",
			defaultDB: "app",
			want:      "select count(*) as total from orders",
		},
		{
			// `SELECT db.t.col AS col FROM db.t` is what catalog hands
			// us for a no-alias view; stripQualifiers reduces the
			// ColName to bare `col`, and stripRedundantAliases must
			// then drop the `AS col` so the catalog form compares
			// equal to the parser-side `SELECT col FROM t`.
			name:      "strips redundant AS after qualifier removal",
			def:       "SELECT `app`.`users`.`id` AS `id` FROM `app`.`users`",
			defaultDB: "app",
			want:      "select id from users",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parser.NormalizeViewDefinition(tt.def, tt.defaultDB)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestNormalizeViewDefinitionParseError(t *testing.T) {
	_, err := parser.NormalizeViewDefinition("not valid sql at all", "app")
	require.Error(t, err)
}

func TestRestoreSelectLower(t *testing.T) {
	t.Run("nil node returns empty", func(t *testing.T) {
		got, err := parser.RestoreSelectLower(nil)
		assert.NoError(t, err)
		assert.Equal(t, "", got)
	})
}
