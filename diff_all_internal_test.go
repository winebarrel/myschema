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
		{
			name:      "REORGANIZE PARTITION: leading-position splice",
			in:        "ALTER TABLE shop.events REORGANIZE PARTITION p1 INTO (\n  partition p1 values less than (15)\n);",
			algorithm: "COPY",
			lock:      "SHARED",
			want:      "ALTER TABLE shop.events ALGORITHM=COPY, LOCK=SHARED, REORGANIZE PARTITION p1 INTO (\n  partition p1 values less than (15)\n);",
		},
		{
			name:      "ADD PARTITION: leading-position splice",
			in:        "ALTER TABLE shop.events ADD PARTITION (\n  partition p2 values less than (20)\n);",
			algorithm: "INPLACE",
			lock:      "NONE",
			want:      "ALTER TABLE shop.events ALGORITHM=INPLACE, LOCK=NONE, ADD PARTITION (\n  partition p2 values less than (20)\n);",
		},
		{
			name:      "DROP PARTITION: leading-position splice",
			in:        "ALTER TABLE shop.events DROP PARTITION p1;",
			algorithm: "INPLACE",
			lock:      "NONE",
			want:      "ALTER TABLE shop.events ALGORITHM=INPLACE, LOCK=NONE, DROP PARTITION p1;",
		},
		{
			name:      "COALESCE PARTITION: leading-position splice",
			in:        "ALTER TABLE shop.events COALESCE PARTITION 2;",
			algorithm: "COPY",
			lock:      "SHARED",
			want:      "ALTER TABLE shop.events ALGORITHM=COPY, LOCK=SHARED, COALESCE PARTITION 2;",
		},
		// Regression: partitionOpInsertPos previously did a global
		// strings.Index for partition keywords, so an ADD COLUMN
		// statement whose COMMENT mentioned 'ADD PARTITION' was
		// misclassified as a partition op and the hints got
		// spliced into the comment literal. The fixed detector
		// only looks at the alter-spec keyword position right
		// after `ALTER TABLE <name>`, so the comment is ignored.
		{
			name:      "ADD COLUMN with COMMENT mentioning ADD PARTITION: trailing splice (not partition op)",
			in:        "ALTER TABLE shop.users ADD COLUMN c INT COMMENT 'see ADD PARTITION docs';",
			algorithm: "INPLACE",
			lock:      "NONE",
			want:      "ALTER TABLE shop.users ADD COLUMN c INT COMMENT 'see ADD PARTITION docs', ALGORITHM=INPLACE, LOCK=NONE;",
		},
		// Regression: the splice index used to be computed from
		// strings.ToUpper(stmt) and indexed back into stmt. ASCII
		// keywords work fine, but a back-ticked table name with
		// non-ASCII letters could change byte length under ToUpper
		// and shift the splice point. The fixed detector indexes
		// raw `stmt` directly and only ToUpper's a short prefix
		// (the keyword itself, ASCII-only).
		{
			name:      "back-ticked non-ASCII table name with REORGANIZE: splice still in the right place",
			in:        "ALTER TABLE shop.`tαble` REORGANIZE PARTITION p1 INTO (\n  partition p1 values less than (15)\n);",
			algorithm: "COPY",
			lock:      "SHARED",
			want:      "ALTER TABLE shop.`tαble` ALGORITHM=COPY, LOCK=SHARED, REORGANIZE PARTITION p1 INTO (\n  partition p1 values less than (15)\n);",
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
