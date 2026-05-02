package myschema_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
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

func TestObjectCount(t *testing.T) {
	c := myschema.ObjectCount{Database: "shop", Tables: 3, Views: 1}
	assert.Equal(t, "database shop", c.DBLabel())
	assert.Equal(t, "3 table(s), 1 view(s)", c.Summary())
}
