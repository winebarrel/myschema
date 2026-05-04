package model_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/winebarrel/myschema/internal/testutil"
	"github.com/winebarrel/myschema/model"
)

func TestDefaultCollationOf(t *testing.T) {
	cases := []struct {
		charset string
		want    string
	}{
		// MySQL 8.0+ defaults that the project actually relies on.
		{"utf8mb4", "utf8mb4_0900_ai_ci"},
		{"latin1", "latin1_swedish_ci"},
		{"ascii", "ascii_general_ci"},
		{"binary", "binary"},
		// utf8 is a deprecated alias for utf8mb3 on 8.0+; both must
		// resolve to the same default so a column declared with either
		// name normalises consistently.
		{"utf8", "utf8mb3_general_ci"},
		{"utf8mb3", "utf8mb3_general_ci"},
		// A charset not in the table returns "" — callers should treat
		// that as "no normalisation possible, keep the value".
		{"made_up_charset", ""},
		// Empty input is benign.
		{"", ""},
		// Mixed-case input still resolves — MySQL identifiers are
		// case-insensitive and we don't want a SQL written as
		// `CHARSET=UTF8MB4` to bypass normalisation.
		{"UTF8MB4", "utf8mb4_0900_ai_ci"},
		{"Latin1", "latin1_swedish_ci"},
	}
	for _, tc := range cases {
		t.Run(tc.charset, func(t *testing.T) {
			assert.Equal(t, tc.want, model.DefaultCollationOf(tc.charset))
		})
	}
}

func TestCharsetOfCollation(t *testing.T) {
	cases := []struct {
		collation string
		want      string
	}{
		{"utf8mb4_0900_ai_ci", "utf8mb4"},
		{"utf8mb4_unicode_ci", "utf8mb4"},
		{"latin1_swedish_ci", "latin1"},
		{"latin1_bin", "latin1"},
		{"utf8mb3_general_ci", "utf8mb3"},
		// `binary` is the special case where charset and collation
		// share the name (no underscore-prefixed form).
		{"binary", "binary"},
		// Mixed-case input still resolves — charset/collation
		// identifiers are case-insensitive in MySQL.
		{"UTF8MB4_0900_AI_CI", "utf8mb4"},
		{"BINARY", "binary"},
		// Empty input is benign.
		{"", ""},
		// Pathological "no underscore, not binary" input falls through
		// to the lowercased value — caller will get an empty result
		// from DefaultCollationOf and skip the collapse.
		{"made_up", "made"},
		// Same fallthrough but no underscore at all → the whole input
		// is returned (lowercased).
		{"weird", "weird"},
		{"WEIRD", "weird"},
	}
	for _, tc := range cases {
		t.Run(tc.collation, func(t *testing.T) {
			assert.Equal(t, tc.want, model.CharsetOfCollation(tc.collation))
		})
	}
}

func TestCollapseDefaultCollation(t *testing.T) {
	utf8mb4 := "utf8mb4"
	defaultColl := "utf8mb4_0900_ai_ci"
	customColl := "utf8mb4_unicode_ci"
	latin1 := "latin1"
	latin1Default := "latin1_swedish_ci"
	madeUp := "made_up_charset"

	cases := []struct {
		name    string
		charset *string
		coll    *string
		want    *string
	}{
		{"both nil → nil", nil, nil, nil},
		{"charset nil, coll set → coll passes through", nil, &defaultColl, &defaultColl},
		{"coll nil, charset set → nil", &utf8mb4, nil, nil},
		{"coll matches charset default → collapsed to nil", &utf8mb4, &defaultColl, nil},
		{"coll differs from charset default → kept", &utf8mb4, &customColl, &customColl},
		{"different charset's default → kept (charset/coll mismatch isn't this function's job)", &latin1, &defaultColl, &defaultColl},
		{"latin1 default → collapsed", &latin1, &latin1Default, nil},
		{"unknown charset → coll kept (no normalisation possible)", &madeUp, &defaultColl, &defaultColl},
	}
	// MySQL identifiers are case-insensitive: `UTF8MB4_0900_AI_CI`
	// in desired SQL collapses just like the canonical form. Pinned
	// outside the table so the inputs can be local string literals.
	upperCharset := "UTF8MB4"
	upperColl := "UTF8MB4_0900_AI_CI"
	cases = append(cases, struct {
		name    string
		charset *string
		coll    *string
		want    *string
	}{"case-insensitive collapse", &upperCharset, &upperColl, nil})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := model.CollapseDefaultCollation(tc.charset, tc.coll)
			if tc.want == nil {
				assert.Nil(t, got)
				return
			}
			if assert.NotNil(t, got) {
				assert.Equal(t, *tc.want, *got)
			}
		})
	}
}

// TestCharsetDefaultCollationsCoverServer pins that
// `model.defaultCollations` (the parser-side hardcoded charset →
// default-collation table) covers every charset the live server
// reports in `information_schema.CHARACTER_SETS`. If MySQL ever ships
// a new charset (or the project bumps the baseline), this test fails
// loudly so the map can be filled in before drift starts.
//
// Requires a reachable MySQL — the suite has no MySQL-free lane any
// more (see Makefile / AGENTS.md).
func TestCharsetDefaultCollationsCoverServer(t *testing.T) {
	db := testutil.ConnectDB(t)
	ctx := context.Background()
	rows, err := db.QueryContext(ctx, `
SELECT CHARACTER_SET_NAME, DEFAULT_COLLATE_NAME
FROM   information_schema.CHARACTER_SETS`)
	require.NoError(t, err)
	defer rows.Close() //nolint:errcheck
	for rows.Next() {
		var charset, want string
		require.NoError(t, rows.Scan(&charset, &want))
		got := model.DefaultCollationOf(charset)
		assert.Equal(t, want, got,
			"model.defaultCollations missing or wrong for charset %q", charset)
	}
	require.NoError(t, rows.Err())
}
