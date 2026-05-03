package parser

import (
	"fmt"
	"regexp"
	"strings"
)

// knownDirectives is the registry of supported -- myschema:<name> comment
// directives. Add new directive names here when they're implemented; the
// validator below errors on any directive whose name isn't listed.
var knownDirectives = map[string]bool{
	"renamed-from": true,
	"execute":      true,
}

var (
	// anyDirectivePattern matches a single-line comment of the form
	// `-- myschema:<name> ...` (with optional leading whitespace and any
	// number of dashes ≥ 2). Used by ValidateDirectives to flag typos.
	// Allows optional whitespace between the colon and the directive
	// name so a slip like `-- myschema: renamed-from old` is still
	// recognised — the per-directive regex (renameDirectivePattern)
	// stays strict, so such a slip turns into a "malformed directive"
	// error instead of being silently ignored.
	anyDirectivePattern = regexp.MustCompile(`(?m)^\s*--+\s*myschema:\s*([a-zA-Z][a-zA-Z0-9_-]*)\b`)

	// renameDirectivePattern captures the old name from a renamed-from
	// directive. Old name is either a bareword identifier or any
	// backtick-quoted blob (so reserved words and names containing
	// hyphens / spaces / etc. round-trip through model.Ident). The full
	// line must match — trailing junk fails the pattern, which the
	// validator turns into an error so a malformed directive doesn't
	// silently degrade into a destructive DROP+CREATE.
	//
	// Schema-qualified old names (`db.tbl`) are intentionally rejected:
	// myschema operates on a single database per invocation, so the
	// directive only ever refers to an object inside that database.
	renameDirectivePattern = regexp.MustCompile("(?m)^\\s*--+\\s*myschema:renamed-from\\s+(`[^`]+`|[A-Za-z_][A-Za-z0-9_$]*)\\s*$")

	// executeDirectivePattern captures the check-SQL body from an
	// execute directive. Group 1 is the rest-of-line after the
	// directive prefix, trimmed of leading whitespace; the body is
	// taken verbatim (free-form SELECT, no escaping or quoting rules).
	// At least one non-space character is required so the body
	// can't be empty.
	executeDirectivePattern = regexp.MustCompile(`(?m)^\s*--+\s*myschema:execute\s+(\S.*?)\s*$`)
)

// ValidateDirectives scans rawSQL for any -- myschema:<name> comment and
// returns an error if (a) <name> isn't in knownDirectives, or (b) the
// directive line is recognised but doesn't match the syntax for that
// directive. Call this once over the whole input before per-statement
// parsing so typos like `-- myschema:renamed-form old` (wrong name) or
// `-- myschema:renamed-from db.tbl` (qualified, unsupported) or
// `-- myschema:renamed-from` (missing arg) fail loudly instead of being
// silently ignored downstream.
//
// Lines are pre-processed the same way the extractors process them:
// leading `/* … */` blocks (and multi-line block state) are stripped so
// a directive after `/* header */` or after a block-close on the same
// line still gets validated — keeping validator and extractor in
// agreement on what counts as a "directive line".
func ValidateDirectives(rawSQL string) error {
	var inBlock bool
	for line := range strings.SplitSeq(rawSQL, "\n") {
		if inBlock {
			idx := strings.Index(line, "*/")
			if idx < 0 {
				continue
			}
			inBlock = false
			line = line[idx+2:]
		}
		trim, opened := reduceLeadingBlocks(strings.TrimSpace(line))
		if opened {
			inBlock = true
			continue
		}
		m := anyDirectivePattern.FindStringSubmatch(trim)
		if m == nil {
			continue
		}
		name := m[1]
		if !knownDirectives[name] {
			return fmt.Errorf("unknown myschema directive %q", name)
		}
		switch name {
		case "renamed-from":
			if !renameDirectivePattern.MatchString(trim) {
				return fmt.Errorf("malformed -- myschema:renamed-from directive: %q (expected exactly one bareword or `backticked` identifier; schema-qualified names are not supported)", strings.TrimSpace(trim))
			}
		case "execute":
			if !executeDirectivePattern.MatchString(trim) {
				return fmt.Errorf("malformed -- myschema:execute directive: %q (expected `-- myschema:execute <check-sql>` with a non-empty check SQL on the same line)", strings.TrimSpace(trim))
			}
		}
	}
	return nil
}

// ExtractExecuteDirective looks at the first non-blank, non-comment-
// stripped line of piece and, if it is an `-- myschema:execute
// <check-sql>` directive, returns the check-SQL body and the
// remainder of the piece (the SQL statement that the directive
// guards) with the directive line removed.
//
// Comment handling mirrors ExtractStmtRenameFrom / ValidateDirectives:
// `--` line comments, `#` line comments, and `/* … */` block comments
// (single-line *and* multi-line) are skipped without breaking the
// directive's anchor, and a directive after a `*/` on the same line
// (`/* header */ -- myschema:execute …`) still counts.
//
// The remainder is returned verbatim — vitess can't parse the
// typical execute payload (CREATE TRIGGER, CREATE PROCEDURE, …) so
// the parser pipeline carries it as raw text.
//
// Multiple `-- myschema:execute` directives in the same leading
// comment block return an error — ambiguous and almost always a typo,
// matching ExtractStmtRenameFrom's "multiple directives" guard.
//
// `ok` is false when the piece doesn't start with an execute
// directive; the caller falls through to the regular vitess parse
// path in that case.
func ExtractExecuteDirective(piece string) (checkSQL, remainder string, ok bool, err error) {
	// Walk the leading lines to find either the execute directive or
	// the first real SQL line. Only an execute directive that sits
	// before any SQL line counts — same shape as ExtractStmtRenameFrom,
	// where the directive must precede the statement it guards.
	lines := strings.Split(piece, "\n")
	var inBlock bool
	var firstCheck string
	firstIdx := -1
	for i, line := range lines {
		// Multi-line block-comment continuation — skip until we hit
		// the closing `*/`, then re-process anything after it.
		if inBlock {
			idx := strings.Index(line, "*/")
			if idx < 0 {
				continue
			}
			inBlock = false
			line = line[idx+2:]
		}
		trim, opened := reduceLeadingBlocks(strings.TrimSpace(line))
		if opened {
			inBlock = true
			continue
		}
		if trim == "" {
			continue
		}
		// `#` line-comments are skipped just like the rename-from
		// extractor does — they don't break the directive's anchor.
		if strings.HasPrefix(trim, "#") {
			continue
		}
		if strings.HasPrefix(trim, "--") {
			m := executeDirectivePattern.FindStringSubmatch(trim)
			if m == nil {
				continue // some other `--` comment, skip
			}
			if firstIdx >= 0 {
				return "", "", false, fmt.Errorf("multiple -- myschema:execute directives in the same leading comment block (%q then %q); only one is allowed", firstCheck, m[1])
			}
			firstCheck = m[1]
			firstIdx = i
			continue
		}
		// Real SQL line reached. If we saw an execute directive in
		// the leading block, return it now; otherwise this is just a
		// regular non-execute piece.
		if firstIdx >= 0 {
			rest := strings.TrimLeft(strings.Join(lines[firstIdx+1:], "\n"), " \t\n")
			return firstCheck, rest, true, nil
		}
		return "", "", false, nil
	}
	if firstIdx >= 0 {
		// No SQL line in the piece at all — the guarded statement is
		// missing. Caller turns the empty remainder into the
		// "missing the SQL statement" error.
		rest := strings.TrimLeft(strings.Join(lines[firstIdx+1:], "\n"), " \t\n")
		return firstCheck, rest, true, nil
	}
	return "", "", false, nil
}

// ExtractStmtRenameFrom returns the old name from a leading
// `-- myschema:renamed-from <old>` comment block at the top of stmtSQL,
// or "" if no such directive is present. The "leading block" includes
// blank lines, `--` line comments, `#` line comments, and `/* … */`
// block comments — both single-line and multi-line — stopping only at
// the first real SQL line. Without that, a stray comment between such
// a line and the directive would defeat the scan and the directive
// would be silently ignored (validator sees it as known, but extractor
// never reaches it).
//
// Returns an error if more than one renamed-from directive appears in
// the leading block — multiple sources is ambiguous and almost always
// a typo, and silently letting the last one win would mask the mistake.
func ExtractStmtRenameFrom(stmtSQL string) (string, error) {
	var oldName string
	var inBlock bool
	stop := false
	for line := range strings.SplitSeq(stmtSQL, "\n") {
		if stop {
			break
		}
		if inBlock {
			idx := strings.Index(line, "*/")
			if idx < 0 {
				continue
			}
			inBlock = false
			// Re-process anything after the closing `*/` so a
			// directive like `*/ -- myschema:renamed-from old`
			// still attaches.
			line = line[idx+2:]
		}
		trim, opened := reduceLeadingBlocks(strings.TrimSpace(line))
		if opened {
			inBlock = true
			continue
		}
		if trim == "" {
			continue
		}
		// Re-classify the reduced trim. Loop because `--` directives
		// can also appear after a stripped `/* … */` on the same line
		// (e.g. `/* header */ -- myschema:renamed-from old`).
		switch {
		case strings.HasPrefix(trim, "--"):
			if m := renameDirectivePattern.FindStringSubmatch(trim); m != nil {
				candidate := stripBackticks(m[1])
				if oldName != "" {
					return "", fmt.Errorf("multiple -- myschema:renamed-from directives on the same statement (%q then %q); only one is allowed", oldName, candidate)
				}
				oldName = candidate
			}
		case strings.HasPrefix(trim, "#"):
			// non-myschema line comment, skip
		default:
			// First real SQL line — stop scanning the leading block.
			stop = true
		}
	}
	return oldName, nil
}

// reduceLeadingBlocks repeatedly strips a leading `/* … */` block from
// trim and returns the remainder. opened is true when a block opens but
// doesn't close on the same line — the caller should set its multi-line
// inBlock state and skip the rest of this line.
func reduceLeadingBlocks(trim string) (rest string, opened bool) {
	for strings.HasPrefix(trim, "/*") {
		idx := strings.Index(trim[2:], "*/")
		if idx < 0 {
			return "", true
		}
		trim = strings.TrimSpace(trim[2+idx+2:])
	}
	return trim, false
}

// InlineRenames separates inline rename directives by the kind of object
// they attach to. Each supported map is keyed by the new (desired) name.
type InlineRenames struct {
	Columns     map[string]string // new column name → old column name
	Indexes     map[string]string // new index  name → old index  name
	Constraints map[string]string // new CHECK constraint name → old name
	ForeignKeys map[string]string // new FK name → old name
	// Unsupported records directives whose target line resolved to an
	// object kind we don't rename via directive (PRIMARY KEY, anonymous
	// FOREIGN KEY) or whose target line shape we couldn't parse. The
	// parser turns these into errors so a typo or mis-positioned
	// directive doesn't silently degrade into a destructive DROP+CREATE.
	//
	// Note: CHECK constraints and FKs *are* supported via Constraints /
	// ForeignKeys above. They go through a typo-guard validation at
	// plan time (the source name must exist on the current side) but
	// the diff still emits DROP+ADD because MySQL has no in-place
	// RENAME CONSTRAINT / RENAME FOREIGN KEY.
	Unsupported []UnsupportedRename
}

// UnsupportedRename is a single inline directive that couldn't be applied.
type UnsupportedRename struct {
	OldName string
	Reason  string
}

// ExtractInlineRenames walks the body of stmtSQL line by line. Each
// `-- myschema:renamed-from <old>` directive line attaches to the next
// non-comment / non-blank line; the resolved kind comes from inspecting
// that line. Whitespace between tokens (KEY/INDEX/CONSTRAINT prefixes
// and the name that follows) is tolerated as Fields() treats any run of
// whitespace as one separator.
//
// Directives that appear in the leading-comment block (before the first
// SQL line) are statement-level — they're handled by
// ExtractStmtRenameFrom, not here, so this function ignores them.
//
// Comment skipping covers `--` line comments, `# ...` line comments,
// and `/* ... */` block comments — both single-line and multi-line.
// A pending directive is preserved across skipped comment lines so it
// still attaches to the next real target line.
func ExtractInlineRenames(stmtSQL string) *InlineRenames {
	out := &InlineRenames{
		Columns:     map[string]string{},
		Indexes:     map[string]string{},
		Constraints: map[string]string{},
		ForeignKeys: map[string]string{},
	}
	var pending string
	var sawSQL bool
	var inBlock bool
	for line := range strings.SplitSeq(stmtSQL, "\n") {
		if inBlock {
			idx := strings.Index(line, "*/")
			if idx < 0 {
				continue
			}
			inBlock = false
			// Re-process anything after the closing `*/` so a
			// directive like `*/ -- myschema:renamed-from old`
			// still attaches.
			line = line[idx+2:]
		}
		trim, opened := reduceLeadingBlocks(strings.TrimSpace(line))
		if opened {
			inBlock = true
			continue
		}
		if trim == "" {
			continue
		}
		// After block-comment reduction the remainder may still be a
		// `--` directive, a `#` comment, or a real SQL line. Classify
		// here, not before the strip — `/* note */ -- myschema:...` is
		// otherwise lost.
		if strings.HasPrefix(trim, "--") {
			if !sawSQL {
				continue // leading directives belong to ExtractStmtRenameFrom
			}
			if m := renameDirectivePattern.FindStringSubmatch(trim); m != nil {
				if pending != "" {
					// Two directives stacked with no SQL line between them:
					// the first never attached to anything. Surface as
					// Unsupported so the parser errors out instead of
					// silently dropping the earlier directive.
					out.Unsupported = append(out.Unsupported, UnsupportedRename{
						OldName: pending,
						Reason:  "directive was followed by another -- myschema:renamed-from before any SQL line attached it",
					})
				}
				pending = stripBackticks(m[1])
			}
			continue
		}
		if strings.HasPrefix(trim, "#") {
			continue
		}
		if !sawSQL {
			sawSQL = true
			continue // first SQL line is the CREATE TABLE … ( opener
		}
		if pending == "" {
			continue
		}
		kind, name := classifyInlineLine(trim)
		switch kind {
		case inlineKindColumn:
			out.Columns[name] = pending
		case inlineKindIndex:
			out.Indexes[name] = pending
		case inlineKindCheck:
			out.Constraints[name] = pending
		case inlineKindFK:
			out.ForeignKeys[name] = pending
		default:
			out.Unsupported = append(out.Unsupported, UnsupportedRename{
				OldName: pending,
				Reason:  "directive does not attach to a renameable target on the next line",
			})
		}
		pending = ""
	}
	if pending != "" {
		// Loop ended (statement body ran out) before the trailing
		// directive saw any SQL line to attach to. Surface so the
		// parser errors instead of silently dropping the directive.
		out.Unsupported = append(out.Unsupported, UnsupportedRename{
			OldName: pending,
			Reason:  "directive at end of statement has no following SQL line to attach to",
		})
	}
	return out
}

type inlineKind int

const (
	inlineKindUnknown inlineKind = iota
	inlineKindColumn
	inlineKindIndex
	inlineKindCheck // CONSTRAINT name CHECK (...)
	inlineKindFK    // CONSTRAINT name FOREIGN KEY (...)
)

// classifyInlineLine inspects a non-comment line inside a CREATE TABLE
// body and returns (kind, name). Tokenisation goes through strings.Fields
// so any run of whitespace (spaces, tabs, mixed) separates tokens.
//
//   - column line: "name TYPE …"                  → (column, "name")
//   - KEY / INDEX line: "KEY name (…)"            → (index,  "name")
//   - UNIQUE / FULLTEXT / SPATIAL prefix forms    → (index,  "name")
//   - CONSTRAINT line: "CONSTRAINT name <body>"   → (constraint, "name")
//   - PRIMARY KEY / anonymous FOREIGN KEY / blank → (unknown, "")
func classifyInlineLine(line string) (inlineKind, string) {
	// Backtick-quoted column names can contain whitespace
	// (e.g. `weird name VARCHAR(64)`); strings.Fields would split the
	// name itself. Detect that shape up front and parse it as a column
	// without going through tokenize. KEY / INDEX / CONSTRAINT / etc.
	// keywords are MySQL reserved tokens, never quoted, so a leading
	// backtick can only be a column name.
	if name, ok := leadingBacktickedIdent(line); ok {
		return inlineKindColumn, name
	}
	// Index / constraint shapes whose *name* is backticked and contains
	// whitespace can't survive strings.Fields tokenisation either. Try
	// a backtick-aware peel for each known prefix before falling
	// through to the standard tokenize-based path.
	if name, ok := backtickedNameAfterPrefix(line, []string{"UNIQUE KEY", "UNIQUE INDEX", "FULLTEXT KEY", "FULLTEXT INDEX", "SPATIAL KEY", "SPATIAL INDEX"}); ok {
		return inlineKindIndex, name
	}
	if name, ok := backtickedNameAfterPrefix(line, []string{"KEY", "INDEX"}); ok {
		return inlineKindIndex, name
	}
	if name, ok := backtickedNameAfterPrefix(line, []string{"CONSTRAINT"}); ok {
		// Distinguish the rename-eligible named-constraint shapes by what
		// follows the backticked name: UNIQUE → index, CHECK → CHECK
		// constraint, FOREIGN [KEY] → FK. Anything else (including
		// `CONSTRAINT name PRIMARY KEY (...)`, whose name MySQL ignores
		// in favour of the fixed "PRIMARY") falls through to "unknown"
		// so the parser flags the directive as unsupported.
		rest := strings.TrimLeft(stripUntilAfterBacktickedName(line, "CONSTRAINT"), " \t")
		upRest := strings.ToUpper(rest)
		switch {
		case strings.HasPrefix(upRest, "UNIQUE"):
			return inlineKindIndex, name
		case strings.HasPrefix(upRest, "CHECK"):
			return inlineKindCheck, name
		case strings.HasPrefix(upRest, "FOREIGN"):
			return inlineKindFK, name
		}
		return inlineKindUnknown, ""
	}
	tokens := tokenize(line)
	if len(tokens) == 0 {
		return inlineKindUnknown, ""
	}
	upper := strings.ToUpper(tokens[0])

	// Two-word index keywords: UNIQUE KEY, UNIQUE INDEX, FULLTEXT KEY,
	// FULLTEXT INDEX, SPATIAL KEY, SPATIAL INDEX. The name is tokens[2].
	// Unnamed forms (`UNIQUE KEY (col)` etc.) put a "(" in tokens[2] —
	// mirror the unnamed guard from the plain KEY / INDEX branch below.
	switch upper {
	case "UNIQUE", "FULLTEXT", "SPATIAL":
		if len(tokens) < 3 {
			return inlineKindUnknown, ""
		}
		w2 := strings.ToUpper(tokens[1])
		if w2 != "KEY" && w2 != "INDEX" {
			return inlineKindUnknown, ""
		}
		if strings.HasPrefix(tokens[2], "(") {
			return inlineKindUnknown, ""
		}
		return inlineKindIndex, stripBackticks(tokens[2])
	}

	switch upper {
	case "PRIMARY":
		// PRIMARY KEY: name is fixed to "PRIMARY", not user-renameable.
		return inlineKindUnknown, ""
	case "FOREIGN":
		// anonymous "FOREIGN KEY (col) …" — no name. Use the
		// CONSTRAINT-prefixed form to give it a name first.
		return inlineKindUnknown, ""
	case "KEY", "INDEX":
		if len(tokens) < 2 {
			return inlineKindUnknown, ""
		}
		// Unnamed `KEY (col)` / `INDEX (col)`: tokens[1] starts with
		// "(" because the column list is the next token. The user must
		// give the index a name to use renamed-from.
		if strings.HasPrefix(tokens[1], "(") {
			return inlineKindUnknown, ""
		}
		return inlineKindIndex, stripBackticks(tokens[1])
	case "CONSTRAINT":
		if len(tokens) < 3 {
			return inlineKindUnknown, ""
		}
		name := stripBackticks(tokens[1])
		// Distinguish the rename-eligible named-constraint shapes by
		// what follows the name. UNIQUE [KEY|INDEX] is a unique *index*
		// (renameable via ALTER TABLE … RENAME INDEX, lands in
		// t.Indexes); CHECK and FOREIGN KEY have no in-place RENAME on
		// MySQL — they're still threaded through as a typo-guard at
		// plan time. `CONSTRAINT name PRIMARY KEY (...)` is also legal
		// MySQL syntax but the user-supplied name is ignored in favour
		// of the fixed "PRIMARY", so it falls through to "unknown".
		switch strings.ToUpper(tokens[2]) {
		case "UNIQUE":
			return inlineKindIndex, name
		case "CHECK":
			return inlineKindCheck, name
		case "FOREIGN":
			return inlineKindFK, name
		}
		return inlineKindUnknown, ""
	case "CHECK":
		// anonymous CHECK (…) — no name to rename. Fall through to
		// "unknown" so the parser flags the directive as unsupported.
		return inlineKindUnknown, ""
	case ")", ");":
		return inlineKindUnknown, ""
	}

	// Default: a column line. The first token is the column name.
	return inlineKindColumn, stripBackticks(tokens[0])
}

// backtickedNameAfterPrefix tries to match a leading keyword sequence
// from `prefixes` (case-insensitive) followed by whitespace and a
// backtick-quoted identifier (which may itself contain whitespace).
// Each prefix entry may be one or more space-separated keyword tokens
// (e.g. "UNIQUE KEY"); runs of whitespace between those tokens — and
// between the final token and the backtick — are tolerated, so
// `UNIQUE   KEY \`also weird\` (id)` works the same as the single-space
// form. Returns the unquoted identifier when matched.
//
// Used by classifyInlineLine to recognise `KEY \`weird name\` (col)`
// etc., where strings.Fields would otherwise split the backticked name.
func backtickedNameAfterPrefix(line string, prefixes []string) (string, bool) {
	for _, p := range prefixes {
		rest, ok := consumeKeywordSequence(line, p)
		if !ok {
			continue
		}
		rest = strings.TrimLeft(rest, " \t")
		if rest == "" || rest[0] != '`' {
			continue
		}
		end := strings.IndexByte(rest[1:], '`')
		if end < 0 {
			continue
		}
		return rest[1 : 1+end], true
	}
	return "", false
}

// consumeKeywordSequence walks the prefix's space-separated keywords
// against `line` (case-insensitive). Between successive keywords, any
// run of `[ \t]+` is allowed. Returns the substring of line that
// follows the last keyword (whitespace not consumed) and ok=true on a
// match. If the head doesn't match, returns (line, false).
func consumeKeywordSequence(line, prefix string) (string, bool) {
	words := strings.Fields(prefix)
	if len(words) == 0 {
		return line, true
	}
	rest := line
	for i, w := range words {
		if i > 0 {
			next := strings.TrimLeft(rest, " \t")
			if len(next) == len(rest) {
				return line, false
			}
			rest = next
		}
		if len(rest) < len(w) || !strings.EqualFold(rest[:len(w)], w) {
			return line, false
		}
		rest = rest[len(w):]
	}
	// The character after the final keyword must be whitespace so we
	// don't match e.g. `KEYWORD` against `KEY`.
	if rest == "" || (rest[0] != ' ' && rest[0] != '\t') {
		return line, false
	}
	return rest, true
}

// stripUntilAfterBacktickedName returns the substring of line that
// follows a leading `<prefix> <backtick-quoted name>` head. Returns
// line as-is if the head doesn't match. Used to inspect what comes
// after a CONSTRAINT name to decide whether it's UNIQUE (a unique-
// index spelling) or another kind.
func stripUntilAfterBacktickedName(line, prefix string) string {
	rest, ok := consumeKeywordSequence(line, prefix)
	if !ok {
		return line
	}
	rest = strings.TrimLeft(rest, " \t")
	if rest == "" || rest[0] != '`' {
		return line
	}
	end := strings.IndexByte(rest[1:], '`')
	if end < 0 {
		return line
	}
	return rest[1+end+1:]
}

// leadingBacktickedIdent returns the unquoted identifier at the start of
// line iff the line begins with a backtick — used to handle column
// definitions whose name is `quoted with spaces`. Identifiers containing
// embedded backticks (MySQL escapes them by doubling: a literal backtick
// inside the quoted name is written as two consecutive backtick chars)
// are not supported by myschema's directive layer: renameDirectivePattern
// stops at the first inner backtick, and the validator turns the
// resulting partial match into a "malformed directive" error. Round-trip
// support would require teaching this helper, the regex, and
// stripBackticks all to recognise the doubling — punted as a vanishingly
// rare case.
func leadingBacktickedIdent(line string) (string, bool) {
	s := strings.TrimLeft(line, " \t")
	if s == "" || s[0] != '`' {
		return "", false
	}
	end := strings.IndexByte(s[1:], '`')
	if end < 0 {
		return "", false
	}
	return s[1 : 1+end], true
}

// tokenize splits `s` into whitespace-separated tokens, then normalises
// each so that strict prefix / equality checks work on names regardless
// of formatting:
//
//   - Trailing "," and "(" are peeled off (e.g. `name(` → `name`).
//   - For tokens that aren't backtick-quoted, anything from the first
//     "(" onwards is dropped (e.g. `idx(col)` → `idx`), so the
//     no-space form `KEY idx(col)` is recognised as an index named
//     `idx`. A token that starts with "(" (e.g. `(col)` from the
//     unnamed `KEY (col)` form) collapses to the empty string and is
//     dropped from the slice.
//   - For tokens that *start* with a backtick AND contain a closing
//     backtick, anything after the closing backtick is dropped (so
//     a token of `select`(id) becomes just `select`), letting the
//     no-space form `KEY <name>(col)` be recognised when <name> is
//     backtick-quoted. Tokens that start with a backtick but have no
//     closing one (a backticked identifier whose body contains
//     whitespace and got split by Fields) are left alone — the
//     column-line path uses leadingBacktickedIdent for those.
//
// Backticks on names are kept so the caller can decide whether to
// strip them.
func tokenize(s string) []string {
	raw := strings.Fields(s)
	out := make([]string, 0, len(raw))
	for _, t := range raw {
		t = strings.TrimRight(t, ",(")
		if t != "" {
			if t[0] == '`' {
				if end := strings.IndexByte(t[1:], '`'); end >= 0 {
					t = t[:end+2]
				}
			} else if i := strings.IndexByte(t, '('); i >= 0 {
				t = t[:i]
			}
		}
		if t == "" {
			continue
		}
		out = append(out, t)
	}
	return out
}

func stripBackticks(s string) string {
	return strings.Trim(s, "`")
}
