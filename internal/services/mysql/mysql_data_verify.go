package mysql

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Verification performs reads only. A partially matching or unidentifiable
// insert stays indeterminate; the caller must never replay it automatically.
func (m *MySQLSessionManager) VerifyMySQLDataEdits(ctx context.Context, id int64, database, table string, result *MySQLDataEditResult) (*MySQLDataVerification, error) {
	if result == nil || len(result.Outcomes) == 0 {
		return nil, ErrMySQLDataPage
	}
	session, qctx, cancel, e := m.requestContext(ctx, id, 30*time.Second)
	if e != nil {
		return nil, e
	}
	defer cancel()
	columns, e := readMySQLColumnMetadata(qctx, session.db, database, table, 128)
	if e != nil {
		return nil, e
	}
	verification := &MySQLDataVerification{Status: "indeterminate"}
	for _, outcome := range result.Outcomes {
		state := "indeterminate"
		candidate := outcome.Saved
		if candidate == nil {
			candidate = outcome.Original
		}
		if candidate != nil {
			if e = mysqlEditValidateRef(qctx, session.db, database, table, candidate.Ref); e != nil {
				return nil, e
			}
			current, e := mysqlEditSnapshot(qctx, session.db, database, table, candidate.Ref, columns, false)
			switch {
			case errors.Is(e, sql.ErrNoRows):
				if outcome.Kind == "delete" {
					state = "applied"
				} else if outcome.Kind == "insert" {
					state = "not_applied"
				}
			case e != nil:
				return nil, e
			case outcome.Kind == "delete":
				if mysqlSnapshotsEqual(current, outcome.Original) {
					state = "not_applied"
				}
			case mysqlSnapshotsEqual(current, outcome.Saved):
				state = "applied"
			case mysqlSnapshotsEqual(current, outcome.Original):
				state = "not_applied"
			}
			// Updating a unique key may leave no row under the candidate key after a
			// rollback. Check the original key before declaring the outcome unknown.
			if state == "indeterminate" && outcome.Kind == "update" && outcome.Original != nil {
				original, e := mysqlEditSnapshot(qctx, session.db, database, table, outcome.Original.Ref, columns, false)
				if e != nil && !errors.Is(e, sql.ErrNoRows) {
					return nil, e
				}
				if mysqlSnapshotsEqual(original, outcome.Original) {
					state = "not_applied"
				}
			}
		}
		verification.States = append(verification.States, state)
	}
	first := verification.States[0]
	same := true
	for _, state := range verification.States {
		if state != first {
			same = false
		}
	}
	if same {
		verification.Status = first
	}
	return verification, nil
}
