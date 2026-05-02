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
	// directive. Old name is a single identifier with optional backticks.
	// Schema-qualified old names (`db.tbl`) are intentionally rejected:
	// myschema operates on a single database per invocation, so the
	// directive only ever refers to an object inside that database.
	renameDirectivePattern = regexp.MustCompile(`(?m)^\s*--+\s*myschema:renamed-from\s+(` + "`?" + `[A-Za-z_][A-Za-z0-9_$]*` + "`?" + `)\s*$`)
)

// ValidateDirectives scans rawSQL for any -- myschema:<name> comment and
// returns an error if <name> isn't in knownDirectives. Call this once over
// the whole input before per-statement parsing so typos like
// `-- myschema:renamed-form old` fail loudly instead of being silently
// ignored.
func ValidateDirectives(rawSQL string) error {
	for _, m := range anyDirectivePattern.FindAllStringSubmatch(rawSQL, -1) {
		name := m[1]
		if !knownDirectives[name] {
			return fmt.Errorf("unknown myschema directive %q", name)
		}
	}
	return nil
}

// ExtractStmtRenameFrom returns the old name from a leading
// `-- myschema:renamed-from <old>` comment block at the top of stmtSQL,
// or "" if no such directive is present. Scans only contiguous comment /
// blank lines at the start; stops at the first SQL line.
func ExtractStmtRenameFrom(stmtSQL string) string {
	var oldName string
	for line := range strings.SplitSeq(stmtSQL, "\n") {
		trim := strings.TrimSpace(line)
		if trim == "" {
			continue
		}
		if !strings.HasPrefix(trim, "--") {
			break
		}
		if m := renameDirectivePattern.FindStringSubmatch(trim); m != nil {
			oldName = stripBackticks(m[1])
		}
	}
	return oldName
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
				pending = stripBackticks(m[1])
			}
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
	tokens := tokenize(line)
	if len(tokens) == 0 {
		return inlineKindUnknown, ""
	}
	upper := strings.ToUpper(tokens[0])

	// Two-word index keywords: UNIQUE KEY, UNIQUE INDEX, FULLTEXT KEY,
	// FULLTEXT INDEX, SPATIAL KEY, SPATIAL INDEX. The name is tokens[2].
	switch upper {
	case "UNIQUE", "FULLTEXT", "SPATIAL":
		if len(tokens) < 3 {
			return inlineKindUnknown, ""
		}
		w2 := strings.ToUpper(tokens[1])
		if w2 != "KEY" && w2 != "INDEX" {
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
		return inlineKindIndex, stripBackticks(tokens[1])
	case "CONSTRAINT":
		if len(tokens) < 2 {
			return inlineKindUnknown, ""
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

// tokenize splits `s` into whitespace-separated tokens, then peels off
// trailing punctuation (",", "(", "(") from each so that strict prefix /
// equality checks work on names regardless of formatting. Backticks on
// names are kept so the caller can decide whether to strip them.
func tokenize(s string) []string {
	raw := strings.Fields(s)
	out := make([]string, 0, len(raw))
	for _, t := range raw {
		t = strings.TrimRight(t, ",(")
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
