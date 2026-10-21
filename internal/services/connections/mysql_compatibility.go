package connections

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// MySQLCompatibility stores only non-secret override metadata. These options
// are not applied until an independent MySQL connection driver is implemented.
type MySQLCompatibility struct {
	Enabled             bool   `json:"enabled,omitempty"`
	ForceLowerCase      bool   `json:"forceLowerCase,omitempty"`
	LowerCaseTableNames string `json:"lowerCaseTableNames"`
	ForceSQLMode        bool   `json:"forceSQLMode,omitempty"`
	SQLMode             string `json:"sqlMode"`
	ForceNDB            bool   `json:"forceNDB,omitempty"`
	NDBSupport          string `json:"ndbSupport"`
	ForceDatabaseMethod bool   `json:"forceDatabaseMethod,omitempty"`
	DatabaseMethod      string `json:"databaseMethod"`
	ForceViewMethod     bool   `json:"forceViewMethod,omitempty"`
	ViewMethod          string `json:"viewMethod"`
}

// This value was copied from the original UI's default, not its source code.
const DefaultMySQLSQLMode = "ONLY_FULL_GROUP_BY, STRICT_TRANS_TABLES, NO_ZERO_IN_DATE, NO_ZERO_DATE, ERROR_FOR_DIVISION_BY_ZERO, NO_ENGINE_SUBSTITUTION"

var (
	MySQLLowerCaseValues  = []string{"0", "1", "2"}
	MySQLNDBSupportValues = []string{"Support", "Not Support"}
	MySQLDatabaseMethods  = []string{"SHOW DATABASES Statement", "INFORMATION_SCHEMA"}
	MySQLViewMethods      = []string{"SHOW FULL TABLES Statement", "INFORMATION_SCHEMA"}
)

func DefaultMySQLCompatibility() MySQLCompatibility {
	return MySQLCompatibility{
		LowerCaseTableNames: "0",
		SQLMode:             DefaultMySQLSQLMode,
		NDBSupport:          "Not Support",
		DatabaseMethod:      "SHOW DATABASES Statement",
		ViewMethod:          "SHOW FULL TABLES Statement",
	}
}

func validateMySQLCompatibility(c MySQLCompatibility) error {
	if c == (MySQLCompatibility{}) {
		return nil // legacy records receive defaults during normalization
	}
	for _, choice := range []struct {
		name    string
		value   string
		options []string
	}{
		{"lower_case_table_names", c.LowerCaseTableNames, MySQLLowerCaseValues},
		{"NDB", c.NDBSupport, MySQLNDBSupportValues},
		{"database listing", c.DatabaseMethod, MySQLDatabaseMethods},
		{"view listing", c.ViewMethod, MySQLViewMethods},
	} {
		valid := false
		for _, option := range choice.options {
			if choice.value == option {
				valid = true
				break
			}
		}
		if !valid {
			return fmt.Errorf("%w: invalid MySQL compatibility %s", ErrInvalid, choice.name)
		}
	}
	if strings.ContainsRune(c.SQLMode, 0) || utf8.RuneCountInString(c.SQLMode) > 4096 {
		return fmt.Errorf("%w: invalid MySQL sql_mode", ErrInvalid)
	}
	return nil
}
