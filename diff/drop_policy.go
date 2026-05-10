package diff

// DropChecker tells the diff engine which categories of drops are allowed.
// This mirrors pistachio's behaviour, but for MySQL the meaningful categories
// are: table, column, constraint, foreign_key, index.
type DropChecker interface {
	IsDropAllowed(kind string) bool
}

// AllowAll permits every drop. Used as the default when no policy is supplied.
type AllowAll struct{}

func (AllowAll) IsDropAllowed(string) bool { return true }

// AllowList allows only the listed kinds. The token "all" matches every kind.
type AllowList struct {
	Kinds map[string]bool
}

func (a *AllowList) IsDropAllowed(kind string) bool {
	if a == nil {
		return false
	}
	if a.Kinds["all"] {
		return true
	}
	return a.Kinds[kind]
}

// normalizeDropChecker substitutes AllowAll for nil so callers don't have to
// nil-check.
func normalizeDropChecker(dc DropChecker) DropChecker {
	if dc == nil {
		return AllowAll{}
	}
	return dc
}
