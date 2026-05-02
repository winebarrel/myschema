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

func TestExtractStmtRenameFromQualifiedNameRejected(t *testing.T) {
	// Qualified old names (`db.tbl`) are intentionally rejected — myschema
	// operates on a single database per invocation, so the directive only
	// ever refers to an object inside that database. The regex doesn't
	// match the `.`, so the directive falls through silently as if it
	// weren't there.
	got := parser.ExtractStmtRenameFrom(`-- myschema:renamed-from other_db.old_users
CREATE TABLE users (id INT);`)
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

func TestExtractInlineRenamesColumns(t *testing.T) {
	got := parser.ExtractInlineRenames(`CREATE TABLE users (
    id BIGINT NOT NULL,
    -- myschema:renamed-from old_name
    name VARCHAR(64) NOT NULL,
    -- myschema:renamed-from old_email
    email VARCHAR(255),
    PRIMARY KEY (id)
);`)
	assert.Equal(t, "old_name", got.Columns["name"])
	assert.Equal(t, "old_email", got.Columns["email"])
	assert.Empty(t, got.Indexes)
	assert.Empty(t, got.Unsupported)
}

func TestExtractInlineRenamesIndexes(t *testing.T) {
	got := parser.ExtractInlineRenames(`CREATE TABLE posts (
    id BIGINT NOT NULL,
    user_id BIGINT NOT NULL,
    PRIMARY KEY (id),
    -- myschema:renamed-from old_idx
    KEY idx_user (user_id),
    -- myschema:renamed-from old_uq
    UNIQUE KEY uq_user (user_id),
    -- myschema:renamed-from old_ft
    FULLTEXT KEY ft_user (user_id),
    -- myschema:renamed-from old_sp
    SPATIAL INDEX sp_user (user_id)
);`)
	assert.Equal(t, "old_idx", got.Indexes["idx_user"])
	assert.Equal(t, "old_uq", got.Indexes["uq_user"])
	assert.Equal(t, "old_ft", got.Indexes["ft_user"])
	assert.Equal(t, "old_sp", got.Indexes["sp_user"])
	assert.Empty(t, got.Columns)
	assert.Empty(t, got.Unsupported)
}

func TestExtractInlineRenamesWhitespaceTolerant(t *testing.T) {
	// Tabs and runs of multiple spaces between KEY/INDEX/UNIQUE keywords
	// and the name must NOT defeat the kind classifier.
	got := parser.ExtractInlineRenames("CREATE TABLE t (\n" +
		"  id INT NOT NULL,\n" +
		"  -- myschema:renamed-from old_idx\n" +
		"  KEY\tidx_x (id),\n" + // tab between KEY and name
		"  -- myschema:renamed-from old_uq\n" +
		"  UNIQUE   KEY   uq_x (id),\n" + // multiple spaces in UNIQUE KEY
		"  PRIMARY KEY (id)\n" +
		");")
	assert.Equal(t, "old_idx", got.Indexes["idx_x"], "tab-separated KEY")
	assert.Equal(t, "old_uq", got.Indexes["uq_x"], "multi-space UNIQUE KEY")
	assert.Empty(t, got.Unsupported)
}

func TestExtractInlineRenamesConstraintIsUnsupported(t *testing.T) {
	// A directive on a CONSTRAINT line is currently surfaced as
	// Unsupported (not silently dropped, not silently mis-attached) so
	// the parser caller can error out — MySQL has no in-place RENAME
	// CONSTRAINT, so falling through to DROP+ADD with the wrong target
	// would be a real regression.
	got := parser.ExtractInlineRenames(`CREATE TABLE t (
    id INT NOT NULL,
    -- myschema:renamed-from old_chk
    CONSTRAINT chk_id CHECK (id > 0),
    PRIMARY KEY (id)
);`)
	assert.Empty(t, got.Columns)
	assert.Empty(t, got.Indexes)
	require.Len(t, got.Unsupported, 1)
	assert.Equal(t, "old_chk", got.Unsupported[0].OldName)
	assert.Contains(t, got.Unsupported[0].Reason, "constraint")
}

func TestExtractInlineRenamesPrimaryKeyIsUnsupported(t *testing.T) {
	got := parser.ExtractInlineRenames(`CREATE TABLE t (
    id INT NOT NULL,
    -- myschema:renamed-from whatever
    PRIMARY KEY (id)
);`)
	require.Len(t, got.Unsupported, 1)
	assert.Contains(t, got.Unsupported[0].Reason, "renameable")
}

func TestExtractInlineRenamesBacktickedNewName(t *testing.T) {
	// New name (the column being renamed *to*) may be backticked too.
	got := parser.ExtractInlineRenames("CREATE TABLE t (\n    -- myschema:renamed-from old\n    `new` INT\n);")
	assert.Equal(t, "old", got.Columns["new"])
}

func TestParseSQLErrorsOnConstraintRenameDirective(t *testing.T) {
	// End-to-end: a -- myschema:renamed-from on a CONSTRAINT line
	// should propagate up as a ParseSQL error, not silently disappear.
	_, err := parser.ParseSQL(`CREATE TABLE t (
    id INT NOT NULL,
    -- myschema:renamed-from old_chk
    CONSTRAINT chk_id CHECK (id > 0),
    PRIMARY KEY (id)
);`, "shop")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "renamed-from")
	assert.Contains(t, err.Error(), "old_chk")
}

func TestParseSQLErrorsOnDanglingRenameDirective(t *testing.T) {
	// Directive that doesn't attach to any renameable line shape
	// (here, immediately above PRIMARY KEY) must error.
	_, err := parser.ParseSQL(`CREATE TABLE t (
    id INT NOT NULL,
    -- myschema:renamed-from whatever
    PRIMARY KEY (id)
);`, "shop")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "renamed-from")
}

func TestExtractInlineRenamesColumnIndexNameCollision(t *testing.T) {
	// MySQL auto-names an unnamed KEY after the first column: `KEY (col)`
	// becomes `KEY col (col)`. If a user has a column also called `col`,
	// then the column-rename directive and the index-rename directive
	// would compete for the same key in a flat map. Kind-aware extraction
	// keeps them separate.
	got := parser.ExtractInlineRenames(`CREATE TABLE t (
    -- myschema:renamed-from old_col
    user_id BIGINT NOT NULL,
    -- myschema:renamed-from old_idx
    KEY user_id (user_id)
);`)
	assert.Equal(t, "old_col", got.Columns["user_id"], "column 'user_id' rename")
	assert.Equal(t, "old_idx", got.Indexes["user_id"], "index 'user_id' rename")
}
