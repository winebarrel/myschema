package model

import (
	"regexp"
	"strings"
)

var safeIdentifierPattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_$]*$`)

var mysqlReservedWords = map[string]struct{}{
	"add": {}, "all": {}, "alter": {}, "analyze": {}, "and": {}, "as": {}, "asc": {},
	"between": {}, "by": {}, "call": {}, "case": {}, "change": {}, "check": {}, "collate": {},
	"column": {}, "constraint": {}, "create": {}, "cross": {}, "current_date": {},
	"current_time": {}, "current_timestamp": {}, "current_user": {}, "database": {},
	"databases": {}, "default": {}, "delete": {}, "desc": {}, "describe": {}, "distinct": {},
	"drop": {}, "else": {}, "exists": {}, "explain": {}, "false": {}, "for": {}, "foreign": {},
	"from": {}, "fulltext": {}, "grant": {}, "group": {}, "having": {}, "if": {}, "ignore": {},
	"in": {}, "index": {}, "inner": {}, "insert": {}, "into": {}, "is": {}, "join": {},
	"key": {}, "keys": {}, "left": {}, "like": {}, "limit": {}, "lock": {}, "match": {},
	"natural": {}, "not": {}, "null": {}, "on": {}, "or": {}, "order": {}, "outer": {},
	"primary": {}, "procedure": {}, "references": {}, "rename": {}, "replace": {},
	"restrict": {}, "revoke": {}, "right": {}, "rlike": {}, "schema": {}, "select": {},
	"set": {}, "show": {}, "table": {}, "then": {}, "to": {}, "trigger": {}, "true": {},
	"union": {}, "unique": {}, "update": {}, "usage": {}, "use": {}, "using": {},
	"values": {}, "view": {}, "when": {}, "where": {}, "with": {},
}

// Ident formats names as MySQL backtick-quoted identifiers joined with dots.
// Empty parts are skipped. Names that are not safe-unquoted or that collide
// with a reserved word are quoted.
func Ident(names ...string) string {
	parts := make([]string, 0, len(names))
	for _, n := range names {
		if n == "" {
			continue
		}
		parts = append(parts, quoteIdent(n))
	}
	return strings.Join(parts, ".")
}

func quoteIdent(name string) string {
	if name == "" {
		return "``"
	}
	if !safeIdentifierPattern.MatchString(name) {
		return quote(name)
	}
	if _, reserved := mysqlReservedWords[strings.ToLower(name)]; reserved {
		return quote(name)
	}
	return name
}

func quote(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}

// QuoteLiteral wraps s in single quotes and escapes embedded single quotes
// and backslashes for MySQL.
func QuoteLiteral(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `'`, `''`)
	return "'" + s + "'"
}
