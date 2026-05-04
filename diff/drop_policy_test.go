package diff_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/winebarrel/myschema/diff"
)

func TestAllowAll_IsDropAllowed(t *testing.T) {
	a := diff.AllowAll{}
	assert.True(t, a.IsDropAllowed("anything"))
	assert.True(t, a.IsDropAllowed(""))
}

func TestAllowList_IsDropAllowed(t *testing.T) {
	t.Run("nil receiver denies", func(t *testing.T) {
		// Calling against a nil pointer is well-defined: returns false
		// rather than panicking. NormalizeDropChecker substitutes
		// AllowAll for nil at the *interface* level, but a typed-nil
		// *AllowList can still reach the receiver method directly.
		var a *diff.AllowList
		assert.False(t, a.IsDropAllowed("table"))
	})
	t.Run("\"all\" wildcard permits any kind", func(t *testing.T) {
		a := &diff.AllowList{Kinds: map[string]bool{"all": true}}
		assert.True(t, a.IsDropAllowed("table"))
		assert.True(t, a.IsDropAllowed("column"))
		assert.True(t, a.IsDropAllowed("anything"))
	})
	t.Run("specific kind only", func(t *testing.T) {
		a := &diff.AllowList{Kinds: map[string]bool{"table": true}}
		assert.True(t, a.IsDropAllowed("table"))
		assert.False(t, a.IsDropAllowed("column"))
	})
	t.Run("empty allow-list denies everything", func(t *testing.T) {
		a := &diff.AllowList{Kinds: map[string]bool{}}
		assert.False(t, a.IsDropAllowed("table"))
		assert.False(t, a.IsDropAllowed("anything"))
	})
}

func TestNormalizeDropChecker(t *testing.T) {
	t.Run("nil substitutes AllowAll", func(t *testing.T) {
		dc := diff.NormalizeDropChecker(nil)
		assert.NotNil(t, dc)
		assert.True(t, dc.IsDropAllowed("anything"))
	})
	t.Run("non-nil passes through unchanged", func(t *testing.T) {
		al := &diff.AllowList{Kinds: map[string]bool{"table": true}}
		dc := diff.NormalizeDropChecker(al)
		assert.True(t, dc.IsDropAllowed("table"))
		assert.False(t, dc.IsDropAllowed("column"))
	})
}
