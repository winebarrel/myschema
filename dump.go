package myschema

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/winebarrel/myschema/catalog"
	"github.com/winebarrel/myschema/model"
	"github.com/winebarrel/orderedmap"
)

// DumpOptions are the flags accepted by `myschema dump`.
type DumpOptions struct {
	FilterOptions
	// `name:"split"` overrides kong's CamelCase→kebab default
	// (`--split-dir`) so the CLI surface matches the documented
	// `--split=<dir>` shape.
	SplitDir string `name:"split" help:"Write one SQL file per table/view into this directory (mkdir -p). Filters apply. SQL is omitted from stdout — only the header summary and a '-- Wrote N file(s) to <dir>' notice are printed."`
}

// DumpResult is the rendered current-schema SQL plus a count for the header.
// SQL is empty when DumpOptions.SplitDir is set — the per-object output lives
// on disk in that case, not in the result.
type DumpResult struct {
	SQL   string
	Count ObjectCount
}

// String makes DumpResult fmt.Stringer-friendly so callers can write it directly.
func (d *DumpResult) String() string { return d.SQL }

// Dump reads the current schema from MySQL and returns it as a SQL string,
// or writes per-object files to DumpOptions.SplitDir when that's set.
func (c *Client) Dump(ctx context.Context, options *DumpOptions) (*DumpResult, error) {
	database, err := c.Database()
	if err != nil {
		return nil, err
	}
	conn, err := c.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close() //nolint:errcheck

	cat := catalog.NewCatalog(conn.Conn, database)
	tables, err := cat.Tables(ctx)
	if err != nil {
		return nil, err
	}
	views, err := cat.Views(ctx)
	if err != nil {
		return nil, err
	}

	tables = filterTables(tables, &options.FilterOptions)
	views = filterViews(views, &options.FilterOptions)

	count := ObjectCount{
		Database: database,
		Tables:   tables.Len(),
		Views:    views.Len(),
	}

	if options.SplitDir != "" {
		if err := writeDumpSplit(options.SplitDir, tables, views); err != nil {
			return nil, err
		}
		// SQL stays empty; cmd/command/dump.go skips writing the body
		// to stdout when SplitDir is set.
		return &DumpResult{Count: count}, nil
	}

	parts := []string{model.TablesToSQL(tables)}
	if views.Len() > 0 {
		parts = append(parts, model.ViewsToSQL(views))
	}
	nonEmpty := parts[:0]
	for _, p := range parts {
		if p != "" {
			nonEmpty = append(nonEmpty, p)
		}
	}
	sql := strings.Join(nonEmpty, "\n\n")

	return &DumpResult{SQL: sql, Count: count}, nil
}

// writeDumpSplit writes one SQL file per table/view into dir,
// mkdir-p'ing the path. Existing files with matching names are
// overwritten; files that no longer correspond to a live object
// (orphans from a previous dump) are deliberately left alone —
// split is a write-this-set, not an rsync-style sync, so the
// operator decides whether to clean up stale files.
//
// Tables and views share the same MySQL identifier namespace, so
// `<name>.sql` is unambiguous (a name can be either a table or a
// view, never both at once).
func writeDumpSplit(dir string, tables *orderedmap.Map[string, *model.Table], views *orderedmap.Map[string, *model.View]) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("dump: mkdir %q: %w", dir, err)
	}
	for _, t := range tables.CollectValues() {
		path, err := splitPath(dir, t.Name)
		if err != nil {
			return err
		}
		body := model.TableToSQL(t) + "\n"
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil { //nolint:gosec // dump output, not a credential file
			return fmt.Errorf("dump: write %q: %w", path, err)
		}
	}
	for _, v := range views.CollectValues() {
		path, err := splitPath(dir, v.Name)
		if err != nil {
			return err
		}
		body := model.ViewToSQL(v) + "\n"
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil { //nolint:gosec
			return fmt.Errorf("dump: write %q: %w", path, err)
		}
	}
	return nil
}

// splitPath returns the per-object output path under dir, refusing any
// object name that filepath.Join would interpret as escaping dir. The
// rejection list is defence-in-depth — MySQL forbids most of these in
// identifiers anyway — but is written against what the *filesystem*
// would do, not what MySQL allows:
//
//   - "" / "." / ".." — path-segment sentinels that, even joined with
//     dir, resolve to dir itself or its parent.
//   - '/' or '\' — embedded path separators would create unintended
//     subdirectories or, on Windows, change the meaning of the join.
//     Checked alongside os.PathSeparator so the Unix and Windows
//     separators are both rejected on every host.
//   - ':' anywhere in the name — on Windows, names like `C:foo`
//     have a volume part that filepath.Join *discards* dir and writes
//     to instead. The check is a literal `:` scan rather than
//     filepath.VolumeName, because that helper is OS-specific
//     (Linux/macOS always return ""), so a Linux-built binary would
//     not catch a Windows-shaped name without the explicit byte
//     scan. filepath.VolumeName is also called as a belt-and-suspenders
//     guard for backslash UNC shapes (`\\server\share\x`) that the
//     '/' / '\' branch above already covers — kept so Windows-only
//     escape vectors fail closed even if someone widens the separator
//     check later.
//
// Names that merely *contain* '.' (e.g. `my.tbl`) are NOT rejected —
// the filesystem treats them as ordinary characters; only the bare
// "." / ".." segment sentinels are unsafe.
func splitPath(dir, name string) (string, error) {
	if name == "" || name == "." || name == ".." ||
		strings.ContainsAny(name, `/\:`) ||
		strings.ContainsRune(name, os.PathSeparator) ||
		filepath.VolumeName(name) != "" {
		return "", fmt.Errorf("dump: refusing unsafe object name %q for split mode", name)
	}
	return filepath.Join(dir, name+".sql"), nil
}
