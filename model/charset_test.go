package model_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
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
