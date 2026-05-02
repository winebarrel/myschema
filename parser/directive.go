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
}

var (
	// anyDirectivePattern matches a single-line comment of the form
	// `-- myschema:<name> ...` (with optional leading whitespace and any
	// number of dashes ≥ 2). Used by ValidateDirectives to flag typos.
	anyDirectivePattern = regexp.MustCompile(`(?m)^\s*--+\s*myschema:([a-zA-Z][a-zA-Z0-9_-]*)\b`)

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
)

// ValidateDirectives scans rawSQL for any -- myschema:<name> comment and
// returns an error if (a) <name> isn't in knownDirectives, or (b) the
// directive line is recognised but doesn't match the syntax for that
// directive. Call this once over the whole input before per-statement
// parsing so typos like `-- myschema:renamed-form old` (wrong name) or
// `-- myschema:renamed-from db.tbl` (qualified, unsupported) or
// `-- myschema:renamed-from` (missing arg) fail loudly instead of being
// silently ignored downstream.
func ValidateDirectives(rawSQL string) error {
	// Walk lines so we can match the *whole* directive line against the
	// directive's required syntax — a partial match anywhere in the
	// line wouldn't tell us about trailing junk or missing arguments.
	for line := range strings.SplitSeq(rawSQL, "\n") {
		m := anyDirectivePattern.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		name := m[1]
		if !knownDirectives[name] {
			return fmt.Errorf("unknown myschema directive %q", name)
		}
		switch name {
		case "renamed-from":
			if !renameDirectivePattern.MatchString(line) {
				return fmt.Errorf("malformed -- myschema:renamed-from directive: %q (expected exactly one bareword or `backticked` identifier; schema-qualified names are not supported)", strings.TrimSpace(line))
			}
		}
	}
	return nil
}

// ExtractStmtRenameFrom returns the old name from a leading
// `-- myschema:renamed-from <old>` comment block at the top of stmtSQL,
// or "" if no such directive is present. The "leading block" includes
// blank lines, `--` line comments, `#` line comments, and single-line
// `/* … */` block comments — stopping only at the first real SQL line.
// Without skipping `#` / `/* … */` a stray comment between such a line
// and the directive would defeat the scan and the directive would be
// silently ignored (validator sees it as known, but extractor never
// reaches it). Multi-line `/* … */` is not unwound; same caveat as in
// ExtractInlineRenames.
//
// Returns an error if more than one renamed-from directive appears in
// the leading block — multiple sources is ambiguous and almost always
// a typo, and silently letting the last one win would mask the mistake.
func ExtractStmtRenameFrom(stmtSQL string) (string, error) {
	var oldName string
	for line := range strings.SplitSeq(stmtSQL, "\n") {
		trim := strings.TrimSpace(line)
		if trim == "" {
			continue
		}
		if strings.HasPrefix(trim, "--") {
			if m := renameDirectivePattern.FindStringSubmatch(trim); m != nil {
				candidate := stripBackticks(m[1])
				if oldName != "" {
					return "", fmt.Errorf("multiple -- myschema:renamed-from directives on the same statement (%q then %q); only one is allowed", oldName, candidate)
				}
				oldName = candidate
			}
			continue
		}
		if strings.HasPrefix(trim, "#") || (strings.HasPrefix(trim, "/*") && strings.HasSuffix(trim, "*/")) {
			continue
		}
		break
	}
	return oldName, nil
}

// InlineRenames separates inline rename directives by the kind of object
// they attach to. Each supported map is keyed by the new (desired) name.
type InlineRenames struct {
	Columns map[string]string // new column name → old column name
	Indexes map[string]string // new index  name → old index  name
	// Unsupported records directives whose target line resolved to an
	// object kind we don't yet rename in place (constraints, FKs,
	// PRIMARY KEY, anonymous FOREIGN KEY) or whose target line shape
	// we couldn't parse. The parser turns these into errors so a typo
	// or mis-positioned directive doesn't silently degrade into a
	// destructive DROP+CREATE.
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
// and `/* ... */` block comments contained on a single line. A
// block comment that spans multiple lines is not unwound here; if a
// directive is followed by a multi-line `/* ... */` block before the
// real target line, the directive will mis-attach. This is rare in
// hand-written CREATE TABLE bodies.
func ExtractInlineRenames(stmtSQL string) *InlineRenames {
	out := &InlineRenames{
		Columns: map[string]string{},
		Indexes: map[string]string{},
	}
	var pending string
	var sawSQL bool
	for line := range strings.SplitSeq(stmtSQL, "\n") {
		trim := strings.TrimSpace(line)
		if trim == "" {
			continue
		}
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
		// Non-myschema comment line: skip without disturbing pending.
		// Covers `#` line comments and single-line `/* … */` blocks
		// that the line-by-line scanner sees as one unit. Multi-line
		// `/* … */` is not unwound — see func doc.
		if strings.HasPrefix(trim, "#") || (strings.HasPrefix(trim, "/*") && strings.HasSuffix(trim, "*/")) {
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
		case inlineKindConstraint:
			out.Unsupported = append(out.Unsupported, UnsupportedRename{
				OldName: pending,
				Reason:  "constraint / FK rename not yet supported (MySQL has no in-place RENAME CONSTRAINT)",
			})
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
	inlineKindConstraint
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
		if len(tokens) < 2 {
			return inlineKindUnknown, ""
		}
		// CONSTRAINT <name> UNIQUE [KEY|INDEX] (...) defines a unique
		// *index* (renameable via ALTER TABLE … RENAME INDEX), not a
		// regular constraint. myschema models UNIQUE this way too —
		// the result lands in t.Indexes, so we route the directive to
		// inlineKindIndex with the constraint/index name.
		if len(tokens) >= 3 && strings.EqualFold(tokens[2], "UNIQUE") {
			return inlineKindIndex, stripBackticks(tokens[1])
		}
		return inlineKindConstraint, stripBackticks(tokens[1])
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
//
// Backticks on names are kept so the caller can decide whether to
// strip them.
func tokenize(s string) []string {
	raw := strings.Fields(s)
	out := make([]string, 0, len(raw))
	for _, t := range raw {
		t = strings.TrimRight(t, ",(")
		if t != "" && t[0] != '`' {
			if i := strings.IndexByte(t, '('); i >= 0 {
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
