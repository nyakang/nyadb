package schemaddl

import "strings"

// Copy uses the native LIKE form; CTAS alone loses defaults and nullability.
// Generated columns and row identity / foreign-key transformations have not
// been accepted for Databend and cannot silently acquire another engine's flags.
func PlanDatabendCopy(original Snapshot, database, destination string, data, rebind, rowID bool) (string, error) {
	if err := validateDatabendOriginal(original, database); err != nil {
		return "", err
	}
	if rebind || rowID || !databendIdentifier(destination) || destination == original.Table.Name || len(original.Databend.Columns) == 0 || len(original.Databend.Columns) > 512 {
		return "", ErrUnsupported
	}
	seen := map[string]bool{}
	var columns []string
	for _, c := range original.Databend.Columns {
		if !databendIdentifier(c.Name) || c.Type == "" || c.Generated || seen[c.Name] {
			return "", ErrUnsupported
		}
		seen[c.Name] = true
		columns = append(columns, Quote(c.Name))
	}
	source := Quote(database) + "." + Quote(original.Table.Name)
	sql := "CREATE TABLE " + Quote(database) + "." + Quote(destination) + " LIKE " + source
	if data {
		sql += " AS SELECT " + strings.Join(columns, ", ") + " FROM " + source
	}
	return sql, nil
}
