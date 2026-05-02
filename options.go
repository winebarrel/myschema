package myschema

import (
	"fmt"
	"path"
)

// Options are the global flags shared by every CLI subcommand.
type Options struct {
	DSN       string   `short:"d" env:"MYSCHEMA_DSN" default:"root@tcp(127.0.0.1:3306)/" help:"MySQL DSN. See https://github.com/go-sql-driver/mysql#dsn-data-source-name"`
	Password  string   `env:"MYSCHEMA_PASSWORD" help:"MySQL password (overrides DSN)."`
	Databases []string `short:"n" env:"MYSCHEMA_DATABASES" required:"" help:"Databases to inspect and modify (comma-separated)."`
}

// FilterOptions narrow the set of objects considered by plan/apply/dump.
type FilterOptions struct {
	Include []string `short:"I" env:"MYSCHEMA_INCLUDE" help:"Include only tables matching the pattern (wildcard: *, ?)."`
	Exclude []string `short:"E" env:"MYSCHEMA_EXCLUDE" help:"Exclude tables matching the pattern."`
}

// MatchName tells whether name passes the include/exclude filters.
func (f *FilterOptions) MatchName(name string) bool {
	if len(f.Include) > 0 {
		matched := false
		for _, p := range f.Include {
			if ok, _ := path.Match(p, name); ok {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	for _, p := range f.Exclude {
		if ok, _ := path.Match(p, name); ok {
			return false
		}
	}
	return true
}

// AfterApply is called by kong; validates the patterns once.
func (f *FilterOptions) AfterApply() error {
	for _, p := range f.Include {
		if _, err := path.Match(p, ""); err != nil {
			return fmt.Errorf("invalid --include pattern %q: %w", p, err)
		}
	}
	for _, p := range f.Exclude {
		if _, err := path.Match(p, ""); err != nil {
			return fmt.Errorf("invalid --exclude pattern %q: %w", p, err)
		}
	}
	return nil
}

// DropPolicy decides which DROP categories the diff is allowed to emit.
type DropPolicy struct {
	AllowDrop []string `env:"MYSCHEMA_ALLOW_DROP" enum:"all,table,column,constraint,foreign_key,index" help:"Comma-separated drop categories to allow."`
}

// IsDropAllowed reports whether kind is in the allow list. The "all" wildcard
// permits every kind.
func (p *DropPolicy) IsDropAllowed(kind string) bool {
	for _, k := range p.AllowDrop {
		if k == "all" || k == kind {
			return true
		}
	}
	return false
}

// ObjectCount summarises how many objects were considered during a run.
type ObjectCount struct {
	Databases []string
	Tables    int
}

func (c ObjectCount) Summary() string {
	return fmt.Sprintf("%d table(s)", c.Tables)
}

func (c ObjectCount) DBLabel() string {
	if len(c.Databases) == 1 {
		return "database " + c.Databases[0]
	}
	return fmt.Sprintf("databases %v", c.Databases)
}
