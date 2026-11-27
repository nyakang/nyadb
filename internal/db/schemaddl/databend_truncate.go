package schemaddl

// PlanDatabendTruncate preserves the original qualified target. This native
// operation has no DELETE affected-row count, CASCADE or identity-reset flags.
func PlanDatabendTruncate(original Snapshot, database string) (string, error) {
	if err := validateDatabendOriginal(original, database); err != nil {
		return "", err
	}
	return "TRUNCATE TABLE " + Quote(database) + "." + Quote(original.Table.Name), nil
}
