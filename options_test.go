package myschema_test

import (
	"testing"

	"github.com/alecthomas/kong"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/winebarrel/myschema"
)

func TestFilterOptionsMatchName(t *testing.T) {
	tests := []struct {
		name    string
		include []string
		exclude []string
		input   string
		want    bool
	}{
		{"no patterns matches anything", nil, nil, "users", true},
		{"include: exact name", []string{"users"}, nil, "users", true},
		{"include: glob match", []string{"user*"}, nil, "users", true},
		{"include: glob no match", []string{"user*"}, nil, "orders", false},
		{"include: multiple patterns OR-ed", []string{"users", "orders"}, nil, "orders", true},
		{"exclude takes precedence", []string{"*"}, []string{"users"}, "users", false},
		{"exclude only: matched -> false", nil, []string{"tmp_*"}, "tmp_log", false},
		{"exclude only: not matched -> true", nil, []string{"tmp_*"}, "users", true},
		{"include + exclude: include matches, exclude wins", []string{"user*"}, []string{"users_archive"}, "users_archive", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &myschema.FilterOptions{Include: tt.include, Exclude: tt.exclude}
			assert.Equal(t, tt.want, f.MatchName(tt.input))
		})
	}
}

func TestFilterOptionsAfterApply(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		f := &myschema.FilterOptions{Include: []string{"good_*"}, Exclude: []string{"*_bad"}}
		assert.NoError(t, f.AfterApply())
	})
	t.Run("invalid include", func(t *testing.T) {
		f := &myschema.FilterOptions{Include: []string{"["}}
		err := f.AfterApply()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "include")
	})
	t.Run("invalid exclude", func(t *testing.T) {
		f := &myschema.FilterOptions{Exclude: []string{"["}}
		err := f.AfterApply()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "exclude")
	})
}

func TestDropPolicyIsDropAllowed(t *testing.T) {
	tests := []struct {
		name  string
		allow []string
		kind  string
		want  bool
	}{
		{"empty list disallows everything", nil, "table", false},
		{"all wildcard", []string{"all"}, "table", true},
		{"all wildcard with other kind", []string{"all"}, "constraint", true},
		{"specific match", []string{"table"}, "table", true},
		{"specific miss", []string{"table"}, "view", false},
		{"multiple kinds, hit", []string{"table", "view"}, "view", true},
		{"multiple kinds, miss", []string{"table", "view"}, "column", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &myschema.DropPolicy{AllowDrop: tt.allow}
			assert.Equal(t, tt.want, p.IsDropAllowed(tt.kind))
		})
	}
}

// regression: `--allow-drop=partition` is only declared in DropPolicy's
// kong enum tag, not in any code path that test fixtures exercise — the
// fixtures populate AllowDrop directly. A typo in the enum tag (or any
// future token rename) would break the CLI without surfacing in CI. This
// test parses the actual flag through kong against the real DropPolicy
// struct so the supported tokens stay in lockstep with documentation.
func TestDropPolicyKongParseAcceptsPartition(t *testing.T) {
	for _, tok := range []string{"all", "table", "view", "column", "constraint", "foreign_key", "index", "partition"} {
		t.Run(tok, func(t *testing.T) {
			var cli struct {
				myschema.DropPolicy
			}
			parser, err := kong.New(&cli)
			require.NoError(t, err)
			_, err = parser.Parse([]string{"--allow-drop=" + tok})
			require.NoError(t, err, "kong must accept --allow-drop=%s as a valid DropPolicy enum value", tok)
			require.Len(t, cli.AllowDrop, 1)
			assert.Equal(t, tok, cli.AllowDrop[0])
		})
	}
}

func TestDropPolicyKongParseRejectsUnknownToken(t *testing.T) {
	// Counterpart to the accept test: an unknown token must be rejected
	// at parse time so a future change can't silently introduce a
	// typo'd kind that AllowDrop accepts but IsDropAllowed never matches.
	var cli struct {
		myschema.DropPolicy
	}
	parser, err := kong.New(&cli)
	require.NoError(t, err)
	_, err = parser.Parse([]string{"--allow-drop=partitions"}) // typo: extra 's'
	require.Error(t, err)
}

func TestObjectCount(t *testing.T) {
	c := myschema.ObjectCount{Database: "shop", Tables: 3, Views: 1}
	assert.Equal(t, "database shop", c.DBLabel())
	assert.Equal(t, "3 table(s), 1 view(s)", c.Summary())
}
