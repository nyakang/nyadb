package mysqlddl

import (
	"strconv"
	"strings"

	"github.com/nyakang/nyadb/internal/db/sqltext"
)

func mariaDBComparisonFields(want, got *Field, actualSQL string) {
	if strings.EqualFold(want.Type, "json") && strings.EqualFold(got.Type, "longtext") && strings.EqualFold(got.Charset, "utf8mb4") && strings.EqualFold(got.Collation, "utf8mb4_bin") {
		name, err := sqltext.Identifier(got.Name, true)
		tokens, tokenErr := sqltext.Tokens(actualSQL)
		comments := false
		for _, token := range tokens {
			comments = comments || token.Comment
		}
		// Recognize only MariaDB's exact JSON alias expansion. Arbitrary
		// LONGTEXT, different checks/collations, and raw attributes remain
		// unequal or unsupported and never acquire verification permission.
		if err == nil && tokenErr == nil && !comments && Canonical(got.Extras) == Canonical("CHECK (json_valid("+name+"))") {
			want.Type = "longtext"
			if want.Charset == "" {
				want.Charset = "utf8mb4"
			}
			if want.Collation == "" {
				want.Collation = "utf8mb4_bin"
			}
			got.Extras, got.Unsupported = "", false
		}
	}
	want.Type, got.Type = mariaDBComparisonType(want.Type), mariaDBComparisonType(got.Type)
}

// MariaDB's SHOW CREATE retains integer display widths (e.g. BIGINT(20)).
// They do not change integer storage or range. ZEROFILL gives width observable
// formatting semantics, so those declarations must still match exactly.
func mariaDBComparisonType(raw string) string {
	if strings.EqualFold(strings.TrimSpace(raw), "bool") || strings.EqualFold(strings.TrimSpace(raw), "boolean") {
		return "tinyint"
	}
	tokens, err := significant(raw)
	if err != nil || len(tokens) < 4 || strings.Contains(strings.ToUpper(raw), "ZEROFILL") {
		return raw
	}
	switch strings.ToLower(tokens[0].Text) {
	case "tinyint", "smallint", "mediumint", "int", "integer", "bigint":
	default:
		return raw
	}
	if tokens[1].Text != "(" || tokens[3].Text != ")" {
		return raw
	}
	width, err := strconv.Atoi(tokens[2].Text)
	if err != nil || width < 1 || width > 255 {
		return raw
	}
	return raw[:tokens[1].Start] + raw[tokens[3].End:]
}
