package myschema

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAppendAlterHints(t *testing.T) {
	cases := []struct {
		name      string
		in        string
		algorithm string
		lock      string
		want      string
	}{
		{
			name: "both empty is a no-op",
			in:   "ALTER TABLE shop.users ADD COLUMN c INT;",
			want: "ALTER TABLE shop.users ADD COLUMN c INT;",
		},
		{
			name:      "ALTER TABLE: comma separators with both clauses",
			in:        "ALTER TABLE shop.users ADD COLUMN c INT;",
			algorithm: "INPLACE",
			lock:      "NONE",
			want:      "ALTER TABLE shop.users ADD COLUMN c INT, ALGORITHM=INPLACE, LOCK=NONE;",
		},
		{
			name:      "ALTER TABLE: only ALGORITHM",
			in:        "ALTER TABLE shop.users DROP COLUMN c;",
			algorithm: "INSTANT",
			want:      "ALTER TABLE shop.users DROP COLUMN c, ALGORITHM=INSTANT;",
		},
		{
			name: "ALTER TABLE: only LOCK",
			in:   "ALTER TABLE shop.users DROP COLUMN c;",
			lock: "NONE",
			want: "ALTER TABLE shop.users DROP COLUMN c, LOCK=NONE;",
		},
		{
			name:      "ALTER TABLE FK drop is also rewritten",
			in:        "ALTER TABLE shop.posts DROP FOREIGN KEY fk_user;",
			algorithm: "INPLACE",
			lock:      "NONE",
			want:      "ALTER TABLE shop.posts DROP FOREIGN KEY fk_user, ALGORITHM=INPLACE, LOCK=NONE;",
		},
		{
			name:      "CREATE INDEX: space separators with both clauses",
			in:        "CREATE INDEX idx_user ON shop.posts (user_id);",
			algorithm: "INPLACE",
			lock:      "NONE",
			want:      "CREATE INDEX idx_user ON shop.posts (user_id) ALGORITHM=INPLACE LOCK=NONE;",
		},
		{
			name:      "CREATE UNIQUE INDEX: space separators",
			in:        "CREATE UNIQUE INDEX uq_email ON shop.users (email);",
			algorithm: "INPLACE",
			lock:      "NONE",
			want:      "CREATE UNIQUE INDEX uq_email ON shop.users (email) ALGORITHM=INPLACE LOCK=NONE;",
		},
		{
			name:      "CREATE FULLTEXT INDEX: space separators",
			in:        "CREATE FULLTEXT INDEX ft ON shop.docs (body);",
			algorithm: "INPLACE",
			lock:      "NONE",
			want:      "CREATE FULLTEXT INDEX ft ON shop.docs (body) ALGORITHM=INPLACE LOCK=NONE;",
		},
		{
			name:      "CREATE SPATIAL INDEX: space separators",
			in:        "CREATE SPATIAL INDEX sp ON shop.places (location);",
			algorithm: "INPLACE",
			lock:      "NONE",
			want:      "CREATE SPATIAL INDEX sp ON shop.places (location) ALGORITHM=INPLACE LOCK=NONE;",
		},
		{
			name:      "CREATE INDEX: only ALGORITHM (no extra trailing space)",
			in:        "CREATE INDEX idx ON shop.posts (user_id);",
			algorithm: "INPLACE",
			want:      "CREATE INDEX idx ON shop.posts (user_id) ALGORITHM=INPLACE;",
		},
		{
			name:      "CREATE TABLE: untouched (online DDL doesn't apply)",
			in:        "CREATE TABLE shop.users (id BIGINT NOT NULL);",
			algorithm: "INPLACE",
			lock:      "NONE",
			want:      "CREATE TABLE shop.users (id BIGINT NOT NULL);",
		},
		{
			name:      "DROP TABLE: untouched",
			in:        "DROP TABLE shop.legacy;",
			algorithm: "INPLACE",
			lock:      "NONE",
			want:      "DROP TABLE shop.legacy;",
		},
		{
			name:      "DROP VIEW: untouched",
			in:        "DROP VIEW shop.v_active;",
			algorithm: "INPLACE",
			lock:      "NONE",
			want:      "DROP VIEW shop.v_active;",
		},
		{
			name:      "CREATE OR REPLACE VIEW: untouched",
			in:        "CREATE OR REPLACE VIEW shop.v AS SELECT 1;",
			algorithm: "INPLACE",
			lock:      "NONE",
			want:      "CREATE OR REPLACE VIEW shop.v AS SELECT 1;",
		},
		{
			name:      "leading whitespace tolerated when matching the prefix",
			in:        "  ALTER TABLE shop.users DROP COLUMN c;",
			algorithm: "INPLACE",
			want:      "  ALTER TABLE shop.users DROP COLUMN c, ALGORITHM=INPLACE;",
		},
		{
			name:      "case-insensitive prefix match",
			in:        "alter table shop.users drop column c;",
			algorithm: "INPLACE",
			want:      "alter table shop.users drop column c, ALGORITHM=INPLACE;",
		},
		{
			name:      "no trailing semicolon: clauses appended at the end",
			in:        "ALTER TABLE shop.users ADD COLUMN c INT",
			algorithm: "INPLACE",
			lock:      "NONE",
			want:      "ALTER TABLE shop.users ADD COLUMN c INT, ALGORITHM=INPLACE, LOCK=NONE",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := appendAlterHints([]string{tc.in}, tc.algorithm, tc.lock)
			assert.Equal(t, tc.want, got[0])
		})
	}
}

func TestAppendAlterHintsEmptyInput(t *testing.T) {
	// Make sure nil / empty slices survive intact (and don't panic).
	assert.Empty(t, appendAlterHints(nil, "INPLACE", "NONE"))
	assert.Empty(t, appendAlterHints([]string{}, "INPLACE", "NONE"))
}
