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

func TestValidateDirectivesRejectsQualifiedRenameTarget(t *testing.T) {
	err := parser.ValidateDirectives(`-- myschema:renamed-from other_db.old_users
CREATE TABLE users (id INT);
`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "malformed")
	assert.Contains(t, err.Error(), "renamed-from")
}

func TestValidateDirectivesRejectsMissingArg(t *testing.T) {
	err := parser.ValidateDirectives(`-- myschema:renamed-from
CREATE TABLE users (id INT);
`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "malformed")
}

func TestValidateDirectivesRejectsSpaceAfterColon(t *testing.T) {
	// `myschema: renamed-from old` with a space after the colon is
	// almost certainly a formatting slip; the validator must catch it
	// instead of silently letting the extractor ignore the directive.
	err := parser.ValidateDirectives(`-- myschema: renamed-from old_users
CREATE TABLE users (id INT);
`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "malformed")
}

func TestValidateDirectivesRejectsTrailingJunk(t *testing.T) {
	err := parser.ValidateDirectives(`-- myschema:renamed-from old, then drop it
CREATE TABLE users (id INT);
`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "malformed")
}

func TestValidateDirectivesAcceptsBacktickedReservedWord(t *testing.T) {
	// Reserved words / hyphens / etc. round-trip through model.Ident
	// when backtick-quoted; the directive's old name should accept the
	// same shape.
	require.NoError(t, parser.ValidateDirectives("-- myschema:renamed-from `select`\nCREATE TABLE users (id INT);\n"))
	require.NoError(t, parser.ValidateDirectives("-- myschema:renamed-from `weird-name`\nCREATE TABLE users (id INT);\n"))
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
	got, err := parser.ExtractStmtRenameFrom(`-- myschema:renamed-from old_users
CREATE TABLE users (id INT);`)
	require.NoError(t, err)
	assert.Equal(t, "old_users", got)
}

func TestExtractStmtRenameFromQualifiedNameDoesNotMatch(t *testing.T) {
	// Qualified old names (`db.tbl`) are intentionally rejected. The
	// regex doesn't match the dotted form, so the extractor returns "".
	// (At the higher level, ValidateDirectives turns the same input into
	// a malformed-directive error — see TestValidateDirectivesRejectsQualifiedRenameTarget.)
	got, err := parser.ExtractStmtRenameFrom(`-- myschema:renamed-from other_db.old_users
CREATE TABLE users (id INT);`)
	require.NoError(t, err)
	assert.Equal(t, "", got)
}

func TestExtractStmtRenameFromBacktickedReservedWord(t *testing.T) {
	// The widened pattern accepts any backtick-quoted blob.
	got, err := parser.ExtractStmtRenameFrom("-- myschema:renamed-from `select`\nCREATE TABLE users (id INT);")
	require.NoError(t, err)
	assert.Equal(t, "select", got)
}

func TestExtractStmtRenameFromSkipsMultiLineBlockComment(t *testing.T) {
	// A multi-line `/* … */` block in the leading comment block must
	// not stop the scan. The directive after the closing `*/` line
	// has to still be picked up as statement-level.
	got, err := parser.ExtractStmtRenameFrom(`/*
 * generated header
 * spans multiple lines
 */
-- myschema:renamed-from old_users
CREATE TABLE users (id INT);`)
	require.NoError(t, err)
	assert.Equal(t, "old_users", got)
}

func TestExtractInlineRenamesSkipsMultiLineBlockComment(t *testing.T) {
	// A multi-line block comment between the directive and the
	// target column must not clear `pending` — the directive should
	// still attach to the column on the line after `*/`.
	got := parser.ExtractInlineRenames(`CREATE TABLE t (
    id INT NOT NULL,
    -- myschema:renamed-from old_name
    /* this header
       spans multiple
       lines */
    name VARCHAR(64) NOT NULL,
    PRIMARY KEY (id)
);`)
	assert.Equal(t, "old_name", got.Columns["name"])
	assert.Empty(t, got.Unsupported)
}

func TestValidateDirectivesAfterLeadingBlockComment(t *testing.T) {
	// A malformed directive after a single-line `/* … */` on the same
	// line must still error — extractors process this shape, so the
	// validator has to as well or typos slip through.
	err := parser.ValidateDirectives("/* header */ -- myschema:renamed-form old\nCREATE TABLE t (id INT);\n")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown")
}

func TestValidateDirectivesAfterMultiLineBlockClose(t *testing.T) {
	// Multi-line block closes on the same line as the directive:
	// `*/ -- myschema:renamed-form old` — the validator must
	// re-scan after `*/`.
	err := parser.ValidateDirectives(`/*
multi
line
*/ -- myschema:renamed-form old
CREATE TABLE t (id INT);
`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown")
}

func TestExtractStmtRenameFromMultiLineBlockCloseSameLineAsDirective(t *testing.T) {
	// `*/ -- myschema:renamed-from old` — the closing `*/` ends a
	// multi-line block and the directive on the rest of the line
	// must still attach.
	got, err := parser.ExtractStmtRenameFrom(`/*
header
*/ -- myschema:renamed-from old_users
CREATE TABLE users (id INT);`)
	require.NoError(t, err)
	assert.Equal(t, "old_users", got)
}

func TestExtractInlineRenamesMultiLineBlockCloseSameLineAsDirective(t *testing.T) {
	got := parser.ExtractInlineRenames(`CREATE TABLE t (
    id INT NOT NULL,
    /*
    multi-line
    note
    */ -- myschema:renamed-from old_name
    name VARCHAR(64) NOT NULL,
    PRIMARY KEY (id)
);`)
	assert.Equal(t, "old_name", got.Columns["name"])
	assert.Empty(t, got.Unsupported)
}

func TestExtractStmtRenameFromBlockCommentBeforeDirectiveSameLine(t *testing.T) {
	// `/* header */ -- myschema:renamed-from old` — the directive
	// follows a single-line block comment on the same line. The
	// reduce-then-classify pass picks up the directive instead of
	// breaking out as "first SQL line".
	got, err := parser.ExtractStmtRenameFrom(`/* header */ -- myschema:renamed-from old_users
CREATE TABLE users (id INT);`)
	require.NoError(t, err)
	assert.Equal(t, "old_users", got)
}

func TestExtractInlineRenamesBlockCommentBeforeDirectiveSameLine(t *testing.T) {
	got := parser.ExtractInlineRenames(`CREATE TABLE t (
    id INT NOT NULL,
    /* note */ -- myschema:renamed-from old_name
    name VARCHAR(64) NOT NULL,
    PRIMARY KEY (id)
);`)
	assert.Equal(t, "old_name", got.Columns["name"])
	assert.Empty(t, got.Unsupported)
}

func TestExtractInlineRenamesBacktickedIndexMultiWhitespacePrefix(t *testing.T) {
	// `UNIQUE   KEY` (multi-space) and `FULLTEXT\tINDEX` (tab) between
	// the two-word keyword. backtickedNameAfterPrefix walks the prefix
	// keyword by keyword with run-of-whitespace separators, so the
	// backticked name is still extracted.
	got := parser.ExtractInlineRenames("CREATE TABLE t (\n" +
		"    id INT NOT NULL,\n" +
		"    -- myschema:renamed-from old_uq\n" +
		"    UNIQUE   KEY `also weird` (id),\n" +
		"    -- myschema:renamed-from old_ft\n" +
		"    FULLTEXT\tINDEX `headline` (id)\n" +
		");")
	assert.Equal(t, "old_uq", got.Indexes["also weird"])
	assert.Equal(t, "old_ft", got.Indexes["headline"])
	assert.Empty(t, got.Unsupported)
}

func TestExtractInlineRenamesBacktickedIndexNameWithSpace(t *testing.T) {
	// MySQL allows backtick-quoted index names with whitespace, e.g.
	// `KEY `weird name` (col)`. strings.Fields would split the name
	// itself; the backticked-name peel handles it without going
	// through tokenize.
	got := parser.ExtractInlineRenames("CREATE TABLE t (\n" +
		"    id INT NOT NULL,\n" +
		"    -- myschema:renamed-from old_idx\n" +
		"    KEY `weird name` (id),\n" +
		"    -- myschema:renamed-from old_uq\n" +
		"    UNIQUE KEY `also weird` (id),\n" +
		"    -- myschema:renamed-from old_chk\n" +
		"    CONSTRAINT `chk one` CHECK (id > 0)\n" +
		");")
	assert.Equal(t, "old_idx", got.Indexes["weird name"])
	assert.Equal(t, "old_uq", got.Indexes["also weird"])
	require.Len(t, got.Unsupported, 1, "constraint with backticked name still surfaces as Unsupported (CHECK rename not in scope)")
	assert.Equal(t, "old_chk", got.Unsupported[0].OldName)
}

func TestExtractStmtRenameFromSkipsLeadingHashAndBlockComments(t *testing.T) {
	// `#` and single-line `/* … */` lines in the leading block must
	// not stop the scan. Without skipping them, the directive after
	// the block comment would be silently lost.
	got, err := parser.ExtractStmtRenameFrom(`# legacy header
/* generated by some tool */
-- myschema:renamed-from old_users
CREATE TABLE users (id INT);`)
	require.NoError(t, err)
	assert.Equal(t, "old_users", got)
}

func TestExtractStmtRenameFromMultipleDirectivesError(t *testing.T) {
	// Two leading renamed-from directives is ambiguous and almost
	// always a typo; better to fail loudly than to let the second one
	// silently win.
	_, err := parser.ExtractStmtRenameFrom(`-- myschema:renamed-from old_users
-- myschema:renamed-from older_users
CREATE TABLE users (id INT);`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "multiple")
}

func TestExtractStmtRenameFromOnlyTopOfBlock(t *testing.T) {
	// Directive after an SQL line must NOT attach.
	got, err := parser.ExtractStmtRenameFrom(`CREATE TABLE users (
    id INT
    -- myschema:renamed-from too_late
);`)
	require.NoError(t, err)
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

func TestExtractInlineRenamesStackedDirectivesUnsupported(t *testing.T) {
	// Two `-- myschema:renamed-from` lines with no SQL line between
	// them — the first never attached, so it must surface as
	// Unsupported instead of being silently overwritten.
	got := parser.ExtractInlineRenames(`CREATE TABLE t (
    id INT NOT NULL,
    -- myschema:renamed-from old_first
    -- myschema:renamed-from old_second
    name VARCHAR(64) NOT NULL,
    PRIMARY KEY (id)
);`)
	require.Len(t, got.Unsupported, 1)
	assert.Equal(t, "old_first", got.Unsupported[0].OldName)
	assert.Equal(t, "old_second", got.Columns["name"])
}

func TestExtractInlineRenamesBacktickedColumnWithSpace(t *testing.T) {
	// Column whose name contains whitespace is backtick-quoted; the
	// backtick-aware first-identifier parser should still pick it up.
	got := parser.ExtractInlineRenames("CREATE TABLE t (\n" +
		"    id INT NOT NULL,\n" +
		"    -- myschema:renamed-from old_weird\n" +
		"    `weird name` VARCHAR(64) NOT NULL,\n" +
		"    PRIMARY KEY (id)\n" +
		");")
	assert.Equal(t, "old_weird", got.Columns["weird name"])
}

func TestParseSQLRejectsRenameDirectiveOnCreateView(t *testing.T) {
	// renamed-from is currently only supported on CREATE TABLE. On any
	// other statement (CREATE VIEW, ALTER TABLE, …) the directive must
	// surface as a parse error so it can't be silently lost.
	_, err := parser.ParseSQL(`-- myschema:renamed-from old_view
CREATE VIEW v AS SELECT 1 AS x;`, "shop")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "CREATE VIEW")
}

func TestExtractInlineRenamesTrailingPendingUnsupported(t *testing.T) {
	// Directive at the very tail of the input — no further line at
	// all. The end-of-loop guard surfaces it as Unsupported instead
	// of silently dropping it. (When the directive is followed by a
	// closing-paren line, the regular default branch reports it as
	// "doesn't attach to a renameable target"; both paths surface as
	// Unsupported, which is the user-visible contract.)
	got := parser.ExtractInlineRenames("CREATE TABLE t (\n    id INT NOT NULL\n    -- myschema:renamed-from trailing")
	require.Len(t, got.Unsupported, 1)
	assert.Equal(t, "trailing", got.Unsupported[0].OldName)
	assert.Contains(t, got.Unsupported[0].Reason, "end of statement")
}

func TestExtractInlineRenamesSkipsBlockAndHashComments(t *testing.T) {
	// A `# comment` and a single-line `/* comment */` line between the
	// directive and its target must NOT clear `pending` or be treated
	// as the SQL line — the directive should still attach to the next
	// real column line.
	got := parser.ExtractInlineRenames(`CREATE TABLE t (
    id INT NOT NULL,
    -- myschema:renamed-from old_name
    # this is a hash comment
    /* and this is a block comment */
    name VARCHAR(64) NOT NULL,
    PRIMARY KEY (id)
);`)
	assert.Equal(t, "old_name", got.Columns["name"])
	assert.Empty(t, got.Unsupported)
}

func TestExtractInlineRenamesUnnamedIndexIsUnsupported(t *testing.T) {
	// `KEY (col)` with no name: tokens after the keyword are "(col)".
	// classifyInlineLine should treat this as unsupported (rather than
	// emit an "index '(col)' not found" error later).
	got := parser.ExtractInlineRenames(`CREATE TABLE t (
    id INT NOT NULL,
    -- myschema:renamed-from old_idx
    KEY (id)
);`)
	assert.Empty(t, got.Indexes)
	require.Len(t, got.Unsupported, 1)
}

func TestExtractInlineRenamesBacktickedNoSpaceIndexNameIsParsed(t *testing.T) {
	// Backtick-quoted index name with no space before the column-list:
	// `KEY `select`(id)`. tokenize must drop the "(id)" suffix after
	// the closing backtick so the index name reads as the bare
	// identifier `select`.
	got := parser.ExtractInlineRenames("CREATE TABLE t (\n" +
		"    id INT NOT NULL,\n" +
		"    -- myschema:renamed-from old_idx\n" +
		"    KEY `select`(id),\n" +
		"    -- myschema:renamed-from old_uq\n" +
		"    UNIQUE KEY `order`(id)\n" +
		");")
	assert.Equal(t, "old_idx", got.Indexes["select"], "backticked no-space `KEY `name`(col)`")
	assert.Equal(t, "old_uq", got.Indexes["order"], "backticked no-space `UNIQUE KEY `name`(col)`")
	assert.Empty(t, got.Unsupported)
}

func TestExtractInlineRenamesNoSpaceIndexNameIsParsed(t *testing.T) {
	// MySQL allows `KEY name(col)` with no space between the name and
	// the column-list opener. tokenize must strip the trailing "(col)"
	// from the bare token so the index name reads as just "name", not
	// "name(col)" (which would later surface as "target index not
	// found").
	got := parser.ExtractInlineRenames(`CREATE TABLE t (
    id INT NOT NULL,
    -- myschema:renamed-from old_idx
    KEY idx_x(id),
    -- myschema:renamed-from old_uq
    UNIQUE KEY uq_x(id)
);`)
	assert.Equal(t, "old_idx", got.Indexes["idx_x"], "no-space form `KEY idx_x(col)`")
	assert.Equal(t, "old_uq", got.Indexes["uq_x"], "no-space form `UNIQUE KEY uq_x(col)`")
	assert.Empty(t, got.Unsupported)
}

func TestExtractInlineRenamesConstraintUniqueIsIndex(t *testing.T) {
	// `CONSTRAINT <name> UNIQUE …` defines a unique *index* in MySQL
	// (renameable via ALTER TABLE … RENAME INDEX) — myschema models it
	// in t.Indexes. The classifier must route the directive to
	// inlineKindIndex with the constraint/index name, not to
	// inlineKindConstraint (which would surface as "constraint rename
	// not supported").
	got := parser.ExtractInlineRenames(`CREATE TABLE t (
    id INT NOT NULL,
    name VARCHAR(64) NOT NULL,
    -- myschema:renamed-from old_uq_a
    CONSTRAINT uq_name UNIQUE KEY (name),
    -- myschema:renamed-from old_uq_b
    CONSTRAINT uq_id UNIQUE (id)
);`)
	assert.Equal(t, "old_uq_a", got.Indexes["uq_name"])
	assert.Equal(t, "old_uq_b", got.Indexes["uq_id"])
	assert.Empty(t, got.Unsupported)
}

func TestExtractInlineRenamesUnnamedTwoWordIndexIsUnsupported(t *testing.T) {
	// Same guard for the two-word index keyword forms (UNIQUE KEY,
	// FULLTEXT INDEX, SPATIAL KEY, etc.). Without the guard,
	// tokens[2] would be "(col)" and the directive would mis-attach
	// to a name of "(col)", later surfacing as "target index not found".
	got := parser.ExtractInlineRenames(`CREATE TABLE t (
    id INT NOT NULL,
    name VARCHAR(64) NOT NULL,
    -- myschema:renamed-from old_uq
    UNIQUE KEY (name),
    -- myschema:renamed-from old_ft
    FULLTEXT INDEX (name)
);`)
	assert.Empty(t, got.Indexes)
	require.Len(t, got.Unsupported, 2)
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
