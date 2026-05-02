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
	// directive. Old name is a single identifier (no spaces, optional
	// backticks). Schema-qualified old names (`db.tbl`) are accepted but
	// unusual — myschema operates on a single database per invocation.
	renameDirectivePattern = regexp.MustCompile(`(?m)^\s*--+\s*myschema:renamed-from\s+(` + "`?" + `[A-Za-z_][A-Za-z0-9_$.` + "`" + `]*` + "`?" + `)\s*$`)
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

// ExtractInlineRenameFrom returns a map from object name → old name for
// every `-- myschema:renamed-from <old>` comment that appears inline in
// stmtSQL (typically inside the parenthesised list of a CREATE TABLE).
// The directive attaches to the first identifier on the next non-comment
// line, which works for both column lines (`name TYPE …`) and constraint
// / KEY lines (`KEY name (…)`, `CONSTRAINT name …`).
func ExtractInlineRenameFrom(stmtSQL string) map[string]string {
	out := map[string]string{}
	var pending string
	for line := range strings.SplitSeq(stmtSQL, "\n") {
		trim := strings.TrimSpace(line)
		if trim == "" {
			continue
		}
		if strings.HasPrefix(trim, "--") {
			if m := renameDirectivePattern.FindStringSubmatch(trim); m != nil {
				pending = stripBackticks(m[1])
			}
			continue
		}
		if pending != "" {
			if name := leadingObjectName(trim); name != "" {
				out[name] = pending
			}
			pending = ""
		}
	}
	return out
}

// leadingObjectName returns the identifier the inline directive attaches
// to: the column name for a column line, or the constraint/key name for a
// CONSTRAINT / KEY / UNIQUE / FOREIGN KEY / PRIMARY KEY / INDEX line.
// Returns "" if the line shape isn't recognised (e.g. an opening paren on
// its own, which we treat as "directive doesn't attach here").
func leadingObjectName(line string) string {
	line = strings.TrimSpace(line)
	if line == "" {
		return ""
	}
	upper := strings.ToUpper(line)
	switch {
	case strings.HasPrefix(upper, "CONSTRAINT "):
		return firstIdent(strings.TrimSpace(line[len("CONSTRAINT "):]))
	case strings.HasPrefix(upper, "PRIMARY KEY"):
		// PRIMARY KEY has no rename target — the constraint is always
		// named "PRIMARY" in MySQL. Skip.
		return ""
	case strings.HasPrefix(upper, "FOREIGN KEY"):
		// Anonymous FK; rename via -- myschema:renamed-from on the
		// CONSTRAINT-prefixed form instead.
		return ""
	case strings.HasPrefix(upper, "UNIQUE KEY ") || strings.HasPrefix(upper, "UNIQUE INDEX "),
		strings.HasPrefix(upper, "FULLTEXT KEY ") || strings.HasPrefix(upper, "FULLTEXT INDEX "),
		strings.HasPrefix(upper, "SPATIAL KEY ") || strings.HasPrefix(upper, "SPATIAL INDEX "):
		// Two-word prefix; skip both words.
		parts := strings.SplitN(line, " ", 3)
		if len(parts) < 3 {
			return ""
		}
		return firstIdent(strings.TrimSpace(parts[2]))
	case strings.HasPrefix(upper, "KEY ") || strings.HasPrefix(upper, "INDEX "):
		parts := strings.SplitN(line, " ", 2)
		if len(parts) < 2 {
			return ""
		}
		return firstIdent(strings.TrimSpace(parts[1]))
	}
	// Default: column line, first token is the column name.
	return firstIdent(line)
}

// firstIdent reads the first identifier off s, handling backtick-quoted
// names. Stops at whitespace, comma, or open-paren.
func firstIdent(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if s[0] == '`' {
		end := strings.IndexByte(s[1:], '`')
		if end < 0 {
			return ""
		}
		return s[1 : 1+end]
	}
	end := strings.IndexAny(s, " \t,(")
	if end < 0 {
		return s
	}
	return s[:end]
}

func stripBackticks(s string) string {
	return strings.Trim(s, "`")
}
