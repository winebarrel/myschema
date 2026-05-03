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
	assert.Equal(t, "old_chk", got.Constraints["chk one"], "backticked CHECK constraint with whitespace name routes to Constraints")
	assert.Empty(t, got.Unsupported)
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

func TestExtractInlineRenamesCheckConstraint(t *testing.T) {
	// A directive on a CONSTRAINT … CHECK line routes to Constraints.
	// MySQL has no in-place RENAME CONSTRAINT, so the diff still emits
	// DROP+ADD; the directive serves as a typo-guard at plan time.
	got := parser.ExtractInlineRenames(`CREATE TABLE t (
    id INT NOT NULL,
    -- myschema:renamed-from old_chk
    CONSTRAINT chk_id CHECK (id > 0),
    PRIMARY KEY (id)
);`)
	assert.Empty(t, got.Columns)
	assert.Empty(t, got.Indexes)
	assert.Empty(t, got.ForeignKeys)
	assert.Equal(t, "old_chk", got.Constraints["chk_id"])
	assert.Empty(t, got.Unsupported)
}

func TestExtractInlineRenamesForeignKey(t *testing.T) {
	// A directive on a CONSTRAINT … FOREIGN KEY line routes to ForeignKeys.
	got := parser.ExtractInlineRenames(`CREATE TABLE posts (
    id BIGINT NOT NULL,
    user_id BIGINT NOT NULL,
    -- myschema:renamed-from old_fk
    CONSTRAINT fk_user FOREIGN KEY (user_id) REFERENCES users(id),
    PRIMARY KEY (id),
    KEY idx_user (user_id)
);`)
	assert.Empty(t, got.Columns)
	assert.Empty(t, got.Constraints)
	assert.Equal(t, "old_fk", got.ForeignKeys["fk_user"])
	assert.Empty(t, got.Unsupported)
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

func TestParseSQLAcceptsConstraintRenameDirective(t *testing.T) {
	// End-to-end: a -- myschema:renamed-from on a CONSTRAINT … CHECK line
	// no longer errors at parse time. The CHECK constraint carries the
	// RenameFrom marker through to the diff layer, which validates the
	// source name against the current side as a typo-guard. ParseSQL
	// itself is happy as long as the directive's target name resolves
	// to a constraint inside the CREATE TABLE body.
	res, err := parser.ParseSQL(`CREATE TABLE t (
    id INT NOT NULL,
    -- myschema:renamed-from old_chk
    CONSTRAINT chk_id CHECK (id > 0),
    PRIMARY KEY (id)
);`, "shop")
	require.NoError(t, err)
	tbl, ok := res.Tables.GetOk("shop.t")
	require.True(t, ok)
	con, ok := tbl.Constraints.GetOk("chk_id")
	require.True(t, ok)
	require.NotNil(t, con.RenameFrom)
	assert.Equal(t, "old_chk", *con.RenameFrom)
}

func TestParseSQLAcceptsForeignKeyRenameDirective(t *testing.T) {
	res, err := parser.ParseSQL(`CREATE TABLE posts (
    id BIGINT NOT NULL,
    user_id BIGINT NOT NULL,
    -- myschema:renamed-from old_fk
    CONSTRAINT fk_user FOREIGN KEY (user_id) REFERENCES users(id),
    PRIMARY KEY (id),
    KEY idx_user (user_id)
);`, "shop")
	require.NoError(t, err)
	tbl, ok := res.Tables.GetOk("shop.posts")
	require.True(t, ok)
	fk, ok := tbl.ForeignKeys.GetOk("fk_user")
	require.True(t, ok)
	require.NotNil(t, fk.RenameFrom)
	assert.Equal(t, "old_fk", *fk.RenameFrom)
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

func TestExtractExecuteDirective(t *testing.T) {
	cases := []struct {
		name     string
		piece    string
		wantOK   bool
		wantCS   string
		wantRest string
	}{
		{
			name: "single-line SQL after directive",
			piece: "-- myschema:execute SELECT 1 FROM information_schema.TRIGGERS WHERE TRIGGER_NAME='trg'\n" +
				"CREATE TRIGGER trg BEFORE INSERT ON t FOR EACH ROW SET NEW.val = 0",
			wantOK:   true,
			wantCS:   "SELECT 1 FROM information_schema.TRIGGERS WHERE TRIGGER_NAME='trg'",
			wantRest: "CREATE TRIGGER trg BEFORE INSERT ON t FOR EACH ROW SET NEW.val = 0",
		},
		{
			name: "leading blank lines + comment skipped before directive",
			piece: "\n\n# comment\n-- myschema:execute SELECT 1\n" +
				"CREATE TRIGGER trg AFTER INSERT ON t FOR EACH ROW SET NEW.val = 1",
			wantOK:   true,
			wantCS:   "SELECT 1",
			wantRest: "CREATE TRIGGER trg AFTER INSERT ON t FOR EACH ROW SET NEW.val = 1",
		},
		{
			name:   "no directive at the head → not an execute group",
			piece:  "CREATE TABLE t (id INT, PRIMARY KEY (id));",
			wantOK: false,
		},
		{
			name:   "directive after a real SQL line is ignored (top-of-piece only)",
			piece:  "CREATE TABLE t (id INT);\n-- myschema:execute SELECT 1\nCREATE TRIGGER tr ...",
			wantOK: false,
		},
		{
			name:   "non-execute -- comment before SQL is skipped, not treated as directive",
			piece:  "-- some plain comment\nCREATE TABLE t (id INT);",
			wantOK: false,
		},
		{
			name: "multi-line block comment header before directive",
			piece: "/*\n * generated header — keep this comment\n */\n" +
				"-- myschema:execute SELECT 1\n" +
				"CREATE TRIGGER trg AFTER INSERT ON t FOR EACH ROW SET NEW.val = 1",
			wantOK:   true,
			wantCS:   "SELECT 1",
			wantRest: "CREATE TRIGGER trg AFTER INSERT ON t FOR EACH ROW SET NEW.val = 1",
		},
		{
			name: "directive after a closed block comment on the same line",
			piece: "/* header */ -- myschema:execute SELECT 1\n" +
				"CREATE TRIGGER trg AFTER INSERT ON t FOR EACH ROW SET NEW.val = 1",
			wantOK:   true,
			wantCS:   "SELECT 1",
			wantRest: "CREATE TRIGGER trg AFTER INSERT ON t FOR EACH ROW SET NEW.val = 1",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cs, rest, ok, err := parser.ExtractExecuteDirective(tc.piece)
			require.NoError(t, err)
			assert.Equal(t, tc.wantOK, ok)
			if tc.wantOK {
				assert.Equal(t, tc.wantCS, cs)
				assert.Equal(t, tc.wantRest, rest)
			}
		})
	}
}

func TestParseSQLAcceptsExecuteDirective(t *testing.T) {
	// End-to-end: ParseSQL parses CREATE TABLE + execute block,
	// landing the trigger SQL on ParseResult.Executes verbatim
	// (with `;` re-appended) without ever handing it to vitess.
	res, err := parser.ParseSQL(`
CREATE TABLE t (id BIGINT NOT NULL, val INT, PRIMARY KEY (id));

-- myschema:execute SELECT 1 FROM information_schema.TRIGGERS WHERE TRIGGER_NAME='trg'
CREATE TRIGGER trg BEFORE INSERT ON t FOR EACH ROW SET NEW.val = 0;
`, "shop")
	require.NoError(t, err)
	require.Len(t, res.Executes, 1)
	eg := res.Executes[0]
	assert.Equal(t, "SELECT 1 FROM information_schema.TRIGGERS WHERE TRIGGER_NAME='trg'", eg.CheckSQL)
	assert.Equal(t, "CREATE TRIGGER trg BEFORE INSERT ON t FOR EACH ROW SET NEW.val = 0;", eg.ExecuteSQL)
	// Tables side still parses cleanly.
	_, ok := res.Tables.GetOk("shop.t")
	assert.True(t, ok)
}

func TestParseSQLAcceptsExecuteDirectiveWithMultilinePayload(t *testing.T) {
	// Single-statement payloads can span multiple lines as long as
	// they don't contain an internal `;` (which would be cut by
	// SplitStatementToPieces — see the multi-statement pin test).
	// A reformatted CREATE TRIGGER for readability must be held on
	// ParseResult.Executes verbatim, newlines and indentation
	// preserved.
	res, err := parser.ParseSQL(`-- myschema:execute SELECT 1
CREATE TRIGGER trg
  BEFORE INSERT ON t
  FOR EACH ROW
  SET NEW.val = 0;
`, "shop")
	require.NoError(t, err)
	require.Len(t, res.Executes, 1)
	assert.Equal(t,
		"CREATE TRIGGER trg\n  BEFORE INSERT ON t\n  FOR EACH ROW\n  SET NEW.val = 0;",
		res.Executes[0].ExecuteSQL,
	)
}

func TestParseSQLRejectsExecuteDirectiveWithEmptyBody(t *testing.T) {
	// A directive whose next non-blank line is empty (no SQL to
	// guard) must error rather than silently swallow the directive.
	_, err := parser.ParseSQL("-- myschema:execute SELECT 1\n", "shop")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "myschema:execute")
	assert.Contains(t, err.Error(), "missing the SQL")
}

func TestValidateDirectivesRejectsMalformedExecute(t *testing.T) {
	// Validator runs before extraction, so an `-- myschema:execute`
	// with no check-SQL on the same line is caught upfront.
	err := parser.ValidateDirectives("-- myschema:execute\nCREATE TRIGGER tr ...")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "malformed -- myschema:execute")
}

func TestExtractExecuteDirectiveRejectsMultipleDirectives(t *testing.T) {
	// Two `-- myschema:execute` directives stacked in the leading
	// comment block is ambiguous (which guards the next statement?).
	// Mirror ExtractStmtRenameFrom's "multiple" guard and error.
	_, _, _, err := parser.ExtractExecuteDirective(
		"-- myschema:execute SELECT 1\n" +
			"-- myschema:execute SELECT 2\n" +
			"CREATE TRIGGER trg BEFORE INSERT ON t FOR EACH ROW SET NEW.val = 0",
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "multiple")
	assert.Contains(t, err.Error(), "execute")
}

func TestParseSQLRejectsExecuteCombinedWithRenameDirective(t *testing.T) {
	// Stacking `myschema:execute` with `myschema:renamed-from` in the
	// same piece is ambiguous — both describe how to act on the next
	// statement, but execute short-circuits the vitess parse so the
	// rename would be silently dropped. Reject upfront.
	_, err := parser.ParseSQL(
		"-- myschema:renamed-from old_trg\n"+
			"-- myschema:execute SELECT 1\n"+
			"CREATE TRIGGER trg BEFORE INSERT ON t FOR EACH ROW SET NEW.val = 0;",
		"shop")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "myschema:execute")
	assert.Contains(t, err.Error(), "renamed-from")
}

func TestParseSQLRejectsExecuteWithNonSelectCheck(t *testing.T) {
	// The check SQL is run on every plan / apply, so accidentally
	// putting DDL/DML there would silently mutate the database.
	// Vitess Parse + type-switch keeps the check read-only.
	cases := []string{
		"DELETE FROM t WHERE id = 1",
		"INSERT INTO t (id) VALUES (1)",
		"DROP TABLE t",
		"SELECT 1; DELETE FROM t",
	}
	for _, ck := range cases {
		t.Run(ck, func(t *testing.T) {
			_, err := parser.ParseSQL(
				"-- myschema:execute "+ck+"\n"+
					"CREATE TRIGGER trg BEFORE INSERT ON t FOR EACH ROW SET NEW.val = 0;",
				"shop")
			require.Error(t, err)
			assert.Contains(t, err.Error(), "myschema:execute")
		})
	}
}

func TestParseSQLAcceptsExecuteWithUnionAndWithCheck(t *testing.T) {
	// UNION and WITH … SELECT are read-only check shapes — both
	// must pass the validator. (vitess folds WITH into the Select
	// shape, so the type switch sees *sqlparser.Select for it.)
	for _, ck := range []string{
		"SELECT 1 UNION SELECT 2",
		"WITH c AS (SELECT 1 AS x) SELECT x FROM c",
	} {
		t.Run(ck, func(t *testing.T) {
			_, err := parser.ParseSQL(
				"-- myschema:execute "+ck+"\n"+
					"CREATE TRIGGER trg BEFORE INSERT ON t FOR EACH ROW SET NEW.val = 0;",
				"shop")
			require.NoError(t, err)
		})
	}
}

func TestParseSQLRejectsExecuteDirectiveWithCommentOnlyBody(t *testing.T) {
	// `executeSQL == ""` after TrimSpace doesn't catch a payload
	// whose only content is `--` / `#` / `/* … */` comments — those
	// are non-empty strings but contain no SQL. MySQL would surface
	// "Query was empty" at apply time; payloadHasNoSQL catches it
	// at parse time instead.
	cases := map[string]string{
		"line comment only":     "-- myschema:execute SELECT 1\n-- just a comment\n",
		"hash comment only":     "-- myschema:execute SELECT 1\n# also just a comment\n",
		"block comment only":    "-- myschema:execute SELECT 1\n/* opaque header */\n",
		"multi-line block only": "-- myschema:execute SELECT 1\n/*\n  spans\n  lines\n*/\n",
		"only blanks and tabs":  "-- myschema:execute SELECT 1\n   \n\t\n",
	}
	for name, sql := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := parser.ParseSQL(sql, "shop")
			require.Error(t, err)
			assert.Contains(t, err.Error(), "myschema:execute")
			assert.Contains(t, err.Error(), "missing the SQL")
		})
	}
}

func TestParseSQLExecutePayloadWithInternalSemicolonsFails(t *testing.T) {
	// CAVEATS pin: vitess's SplitStatementToPieces cuts on every `;`,
	// so a `CREATE TRIGGER … BEGIN …; …; END;` body splits into
	// multiple pieces and the orphan inner pieces (`SET NEW.val = …`,
	// `END`) can't parse on their own. Documented as a v1 limit on
	// the directive — equivalent to MySQL CLI requiring `DELIMITER //`
	// for multi-statement bodies. This test pins the failure mode so
	// future changes that add multi-statement support do so
	// deliberately rather than by accident.
	_, err := parser.ParseSQL(`CREATE TABLE t (id INT, val INT, PRIMARY KEY(id));

-- myschema:execute SELECT 1
CREATE TRIGGER trg BEFORE INSERT ON t FOR EACH ROW BEGIN
  SET NEW.val = 0;
  SET NEW.val = NEW.val + 1;
END;
`, "shop")
	require.Error(t, err)
}
