package model

import "strings"

// DefaultCollationOf returns the default collation MySQL 8.0+ assigns
// to the named character set when no COLLATE clause is specified.
// Returns "" for charsets not in the built-in table; callers should
// treat that as "no normalisation possible — keep the value as-is".
//
// The map matches `information_schema.CHARACTER_SETS.DEFAULT_COLLATE_NAME`
// on stock MySQL 8.0. Older servers (notably 5.7, where utf8mb4 defaulted
// to utf8mb4_general_ci) are not covered — the project baseline is 8.0.
//
// `utf8` is mapped to the `utf8mb3` default because MySQL 8.0+ treats
// `utf8` as a deprecated alias for `utf8mb3`.
//
// Input is lower-cased before lookup so callers don't have to worry
// about whether they pulled the value from the SQL text (where case
// can vary) or from information_schema (always lower-case).
func DefaultCollationOf(charset string) string {
	return defaultCollations[strings.ToLower(charset)]
}

// CollapseDefaultCollation returns coll unchanged unless it equals the
// MySQL-default collation for the given charset, in which case it
// returns nil. Used by parser and catalog alike so a column or table
// declared as `CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci` compares
// equal to the same object declared as bare `CHARSET=utf8mb4` — both
// describe the same MySQL state.
//
// Both inputs are compared case-insensitively (charset and collation
// identifiers are case-insensitive in MySQL); the returned pointer is
// the original `coll` pointer when nothing collapses, so caller-side
// pointer identity / explicit-casing is preserved unless the value
// actually equals the default.
func CollapseDefaultCollation(charset, coll *string) *string {
	if charset == nil || coll == nil {
		return coll
	}
	if def := DefaultCollationOf(*charset); def != "" && strings.EqualFold(def, *coll) {
		return nil
	}
	return coll
}

var defaultCollations = map[string]string{
	"armscii8": "armscii8_general_ci",
	"ascii":    "ascii_general_ci",
	"big5":     "big5_chinese_ci",
	"binary":   "binary",
	"cp1250":   "cp1250_general_ci",
	"cp1251":   "cp1251_general_ci",
	"cp1256":   "cp1256_general_ci",
	"cp1257":   "cp1257_general_ci",
	"cp850":    "cp850_general_ci",
	"cp852":    "cp852_general_ci",
	"cp866":    "cp866_general_ci",
	"cp932":    "cp932_japanese_ci",
	"dec8":     "dec8_swedish_ci",
	"eucjpms":  "eucjpms_japanese_ci",
	"euckr":    "euckr_korean_ci",
	"gb18030":  "gb18030_chinese_ci",
	"gb2312":   "gb2312_chinese_ci",
	"gbk":      "gbk_chinese_ci",
	"geostd8":  "geostd8_general_ci",
	"greek":    "greek_general_ci",
	"hebrew":   "hebrew_general_ci",
	"hp8":      "hp8_english_ci",
	"keybcs2":  "keybcs2_general_ci",
	"koi8r":    "koi8r_general_ci",
	"koi8u":    "koi8u_general_ci",
	"latin1":   "latin1_swedish_ci",
	"latin2":   "latin2_general_ci",
	"latin5":   "latin5_turkish_ci",
	"latin7":   "latin7_general_ci",
	"macce":    "macce_general_ci",
	"macroman": "macroman_general_ci",
	"sjis":     "sjis_japanese_ci",
	"swe7":     "swe7_swedish_ci",
	"tis620":   "tis620_thai_ci",
	"ucs2":     "ucs2_general_ci",
	"ujis":     "ujis_japanese_ci",
	"utf16":    "utf16_general_ci",
	"utf16le":  "utf16le_general_ci",
	"utf32":    "utf32_general_ci",
	"utf8":     "utf8mb3_general_ci",
	"utf8mb3":  "utf8mb3_general_ci",
	"utf8mb4":  "utf8mb4_0900_ai_ci",
}
