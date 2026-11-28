package contract

import "math"

// AddAffectedRows combines reported counts without turning an unreported
// negative count or an overflowing total into a fabricated number of rows.
// Original per-statement values remain in StatementOutcome and native receipts.
func AddAffectedRows(total, count int64) int64 {
	if total < 0 || count < 0 || count > math.MaxInt64-total {
		return -1
	}
	return total + count
}
