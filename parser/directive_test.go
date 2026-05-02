package parser_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/winebarrel/myschema/parser"
)

func TestValidateDirectivesAcceptsKnown(t *testing.T) {
	require.NoError(t, parser.ValidateDirectives(`
-- myschema:renamed-from old_users
CREATE TABLE users (id INT);
`))
}

func TestValidateDirectivesRejectsUnknown(t *testing.T) {
	err := parser.ValidateDirectives(`
-- myschema:renamed-form old_users
CREATE TABLE users (id INT);
`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown myschema directive")
	assert.Contains(t, err.Error(), "renamed-form")
}

func TestValidateDirectivesIgnoresPlainComments(t *testing.T) {
	// Plain SQL comments and unrelated tool comments must be untouched.
	require.NoError(t, parser.ValidateDirectives(`
-- regular comment
-- pist:renamed-from old_users   -- pistachio's prefix, not ours; skip
CREATE TABLE users (id INT);
`))
}

func TestExtractStmtRenameFromBasic(t *testing.T) {
	got := parser.ExtractStmtRenameFrom(`-- myschema:renamed-from old_users
CREATE TABLE users (id INT);`)
	assert.Equal(t, "old_users", got)
}

func TestExtractStmtRenameFromBacktickedOldName(t *testing.T) {
	got := parser.ExtractStmtRenameFrom("-- myschema:renamed-from `weird name`\nCREATE TABLE t (id INT);")
	// firstIdent doesn't accept space inside backticks for the regex —
	// `[A-Za-z0-9_$.]` doesn't match space, so this should fall through
	// as no match. Document the limitation by asserting empty.
	assert.Equal(t, "", got)
}

func TestExtractStmtRenameFromOnlyTopOfBlock(t *testing.T) {
	// Directive after an SQL line must NOT attach.
	got := parser.ExtractStmtRenameFrom(`CREATE TABLE users (
    id INT
    -- myschema:renamed-from too_late
);`)
	assert.Equal(t, "", got)
}

func TestExtractInlineRenameFromColumns(t *testing.T) {
	got := parser.ExtractInlineRenameFrom(`CREATE TABLE users (
    id BIGINT NOT NULL,
    -- myschema:renamed-from old_name
    name VARCHAR(64) NOT NULL,
    -- myschema:renamed-from old_email
    email VARCHAR(255),
    PRIMARY KEY (id)
);`)
	assert.Equal(t, "old_name", got["name"])
	assert.Equal(t, "old_email", got["email"])
	assert.Len(t, got, 2)
}

func TestExtractInlineRenameFromKeyAndConstraint(t *testing.T) {
	got := parser.ExtractInlineRenameFrom(`CREATE TABLE posts (
    id BIGINT NOT NULL,
    user_id BIGINT NOT NULL,
    PRIMARY KEY (id),
    -- myschema:renamed-from old_idx
    KEY idx_user (user_id),
    -- myschema:renamed-from old_uq
    UNIQUE KEY uq_user (user_id),
    -- myschema:renamed-from old_chk
    CONSTRAINT chk_id CHECK (id > 0)
);`)
	assert.Equal(t, "old_idx", got["idx_user"])
	assert.Equal(t, "old_uq", got["uq_user"])
	assert.Equal(t, "old_chk", got["chk_id"])
}

func TestExtractInlineRenameFromBacktickedNewName(t *testing.T) {
	// New name (the column being renamed *to*) may be backticked too.
	got := parser.ExtractInlineRenameFrom("CREATE TABLE t (\n    -- myschema:renamed-from old\n    `new` INT\n);")
	assert.Equal(t, "old", got["new"])
}
