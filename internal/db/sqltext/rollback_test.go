package sqltext

import "testing"

func TestRollbackDoesNotBypassNamespaceForApplicationQueries(t *testing.T) {
	for _, q := range []string{"rollback;", "-- recover\nROLLBACK TO SAVEPOINT original; -- done", `ROLLBACK WORK TO "quoted savepoint"`, "ABORT", "ROLLBACK AND NO CHAIN"} {
		if !IsRollback(q) {
			t.Fatal("recovery command unrecognized", q)
		}
	}
	for _, q := range []string{"ROLLBACK; SELECT 1", "ROLLBACK PREPARED 'tx'", "ROLLBACK TO SAVEPOINT x; SELECT 1", "SELECT 'ROLLBACK'", "'ROLLBACK'", "ROLLBACK TO", "ROLLBACK TO $$; SELECT 1$$"} {
		if IsRollback(q) {
			t.Fatal("application query classified as transaction recovery", q)
		}
	}
}
