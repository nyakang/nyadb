package schemaddl

import (
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/sqltext"
)

// DatabendTableIdentity retains the observed native table incarnation. The
// complete declaration remains opaque: rename does not rebuild its columns.
type DatabendTableIdentity struct {
	Database, TableID string
}

func databendIdentifier(s string) bool {
	return sqltext.ValidName(s) && utf8.ValidString(s) && Identifier(s) == nil
}

// DatabendTableBody removes only the exact native table-name prefix. All
// remaining declaration bytes must survive rename unchanged. Unknown native
// formatting is rejected instead of normalizing SQL or dropping properties.
func DatabendTableBody(ddl, name string) (string, error) {
	if !databendIdentifier(name) {
		return "", ErrUnsupported
	}
	for _, prefix := range []string{"CREATE TABLE " + Quote(name) + " (", "CREATE TABLE " + name + " ("} {
		if strings.HasPrefix(ddl, prefix) && len(ddl) <= 1<<20 && strings.HasSuffix(ddl, ") ENGINE=FUSE") {
			return "(" + strings.TrimPrefix(ddl, prefix), nil
		}
	}
	return "", ErrUnsupported
}

func validateDatabendOriginal(original Snapshot, database string) error {
	i := original.Databend
	if original.Engine != "databend" || !original.Exists || original.Revision == "" || i == nil || i.Database != database ||
		original.Postgres != nil || original.OpenGauss != nil || original.SQLite != nil || !databendIdentifier(database) {
		return ErrUnsupported
	}
	id, err := strconv.ParseUint(i.TableID, 10, 64)
	if err != nil || id == 0 || strconv.FormatUint(id, 10) != i.TableID {
		return ErrUnsupported
	}
	_, err = DatabendTableBody(original.DDL, original.Table.Name)
	return err
}

func PlanDatabendRename(original Snapshot, database, destination string) (string, error) {
	if err := validateDatabendOriginal(original, database); err != nil {
		return "", err
	}
	if !databendIdentifier(destination) || destination == original.Table.Name {
		return "", ErrUnsupported
	}
	// Native ALTER TABLE requires an unqualified destination, in the source
	// database. Qualification here is not interchangeable with PostgreSQL SQL.
	return "ALTER TABLE " + Quote(database) + "." + Quote(original.Table.Name) + " RENAME TO " + Quote(destination), nil
}

func PlanDatabendDrop(original Snapshot, database string) (string, error) {
	if err := validateDatabendOriginal(original, database); err != nil {
		return "", err
	}
	// No IF EXISTS: an absent/replaced original cannot acknowledge this request.
	// Databend does not provide PostgreSQL RESTRICT/CASCADE dependency semantics.
	return "DROP TABLE " + Quote(database) + "." + Quote(original.Table.Name), nil
}
