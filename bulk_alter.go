package myschema

import "strings"

// combineSameTableAlters merges *consecutive* same-table single-spec
// `ALTER TABLE` statements into one multi-spec ALTER. Opt-in via
// `--bulk-alter` on plan / apply (AlterOption.BulkAlter); the default
// is unchanged. dump does not embed AlterOption — bulk-alter is a
// diff-time concern and dump emits whole CREATE TABLE / CREATE VIEW
// statements with no ALTER folding to do.
//
// Statements that aren't combinable act as run separators: they pass
// through untouched and break any in-progress combine group. The
// combinable shape is `ALTER TABLE <ident> <single-spec>[;]` whose
// spec body is *not* a partition op. Specifically excluded:
//
//   - Partition ops (REORGANIZE / ADD / DROP / COALESCE / TRUNCATE /
//     EXCHANGE PARTITION) — MySQL's grammar rejects the trailing-comma
//     multi-spec form for these clauses; detected via partitionOpInsertPos.
//   - Standalone `CREATE INDEX` — different statement shape; the diff
//     layer emits it as its own statement so it can carry the
//     CREATE-INDEX flavour of `--alter-algorithm` / `--alter-lock`.
//   - `CREATE TABLE`, `DROP TABLE`, `RENAME TABLE`, anything else —
//     not `ALTER TABLE` at all.
//
// FK adds and drops are also outside the combinable set in practice,
// but for a different reason: diff_all.go routes them through the
// dedicated `FKAddStmts` / `FKDropStmts` buckets, so they never land
// in the slice this function processes. (See CAVEATS.md "Bulk-alter
// does not combine FK operations" for the rationale: cross-table FK
// adds need to run after every table change, so combining them with
// column changes would destroy the global FK ordering invariant.)
//
// The function is order-preserving — it never reorders statements,
// so the diff's own ordering invariants (e.g. RenameStmts before
// FKDropStmts before Stmts before FKAddStmts) are not at risk.
func combineSameTableAlters(stmts []string) []string {
	if len(stmts) < 2 {
		return stmts
	}
	out := make([]string, 0, len(stmts))

	var (
		groupIdent string
		groupSpecs []string
		groupSemi  bool
	)
	flush := func() {
		switch len(groupSpecs) {
		case 0:
			return
		case 1:
			// One-element group: emit the original spec as a fresh
			// `ALTER TABLE <ident> <spec>` (no point routing through
			// the join below). Trailing `;` carried from the source.
			term := ""
			if groupSemi {
				term = ";"
			}
			out = append(out, "ALTER TABLE "+groupIdent+" "+groupSpecs[0]+term)
		default:
			term := ""
			if groupSemi {
				term = ";"
			}
			out = append(out, "ALTER TABLE "+groupIdent+" "+strings.Join(groupSpecs, ", ")+term)
		}
		groupIdent = ""
		groupSpecs = nil
		groupSemi = false
	}

	for _, stmt := range stmts {
		ident, spec, hasSemi, ok := splitCombinableAlter(stmt)
		if !ok {
			flush()
			out = append(out, stmt)
			continue
		}
		if len(groupSpecs) > 0 && ident == groupIdent {
			groupSpecs = append(groupSpecs, spec)
			continue
		}
		flush()
		groupIdent = ident
		groupSpecs = []string{spec}
		groupSemi = hasSemi
	}
	flush()
	return out
}

// splitCombinableAlter parses an `ALTER TABLE <ident> <spec>[;]` into
// its identifier (as written, including any back-ticks) and spec body.
// hasSemi reports whether the input ended in `;`; the caller carries
// that through to the merged output. ok is false when:
//
//   - stmt isn't `ALTER TABLE …` (case-insensitive prefix);
//   - the spec body is empty;
//   - the spec is a partition op (detected via partitionOpInsertPos).
//
// Identifier parsing reuses skipQualifiedIdentifier, so a `db.table`
// chain or back-ticked identifier is handled exactly the same way as
// in `appendAlterHints`.
func splitCombinableAlter(stmt string) (ident, spec string, hasSemi bool, ok bool) {
	s := strings.TrimRight(stmt, " \t\n\r")
	if strings.HasSuffix(s, ";") {
		hasSemi = true
		s = strings.TrimSuffix(s, ";")
	}
	pos := skipASCIIWhitespace(s, 0)
	if !hasPrefixFold(s, pos, "ALTER TABLE ") {
		return "", "", false, false
	}
	// partitionOpInsertPos inspects the ORIGINAL stmt (with `;`) so the
	// shared helper's offsets stay sane; a partition op anywhere after
	// the table name disqualifies the whole stmt.
	if partitionOpInsertPos(stmt) > 0 {
		return "", "", false, false
	}
	pos += len("ALTER TABLE ")
	pos = skipASCIIWhitespace(s, pos)
	identStart := pos
	pos = skipQualifiedIdentifier(s, pos)
	if pos == identStart {
		return "", "", false, false
	}
	identEnd := pos
	pos = skipASCIIWhitespace(s, pos)
	specBody := strings.TrimSpace(s[pos:])
	if specBody == "" {
		return "", "", false, false
	}
	return s[identStart:identEnd], specBody, hasSemi, true
}
