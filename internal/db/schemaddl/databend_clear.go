package schemaddl

// Clear executes DELETE on the confirmed original; it neither substitutes
// TRUNCATE nor invents SQLite identity-reset semantics for Databend.
func PlanDatabendClear(original Snapshot, database string, resetIdentity bool) (string, error) {
	if err := validateDatabendOriginal(original, database); err != nil {
		return "", err
	}
	if resetIdentity {
		return "", ErrUnsupported
	}
	return "DELETE FROM " + Quote(database) + "." + Quote(original.Table.Name), nil
}
