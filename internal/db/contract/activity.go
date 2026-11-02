package contract

import (
	"context"
	"errors"
	"time"
)

// ActivityRequest contains only immutable, non-secret operation metadata.
// Summary SQL is parameterized or descriptive and must not be replayed.
type ActivityRequest struct {
	ProfileID                     int64
	Generation                    uint64
	Database, Object, Source, SQL string
	Summary                       bool
	Started                       time.Time
}

type ActivityResult struct {
	State, Error       string
	Rows, AffectedRows int64
}

// ActivityRecorder is called on the UI thread before work starts. Its returned
// completion function owns a private receipt channel; a worker calls it once,
// including after cancellation or disposal of the original view.
type ActivityRecorder func(ActivityRequest) func(ActivityResult)

func ActivityOutcome(err error) ActivityResult {
	out := ActivityResult{State: "success"}
	if err == nil {
		return out
	}
	out.State, out.Error = "error", err.Error()
	if errors.Is(err, context.Canceled) {
		out.State = "cancelled"
	}
	if errors.Is(err, ErrRevoked) || errors.Is(err, ErrSQLSessionLost) || errors.Is(err, ErrDataTransactionLost) {
		out.State = "session_lost"
	}
	return out
}

func CompleteActivity(complete func(ActivityResult), result ActivityResult) {
	if complete != nil {
		complete(result)
	}
}
