package contract

import (
	"encoding/json"
	"math"
	"slices"
)

type DataWriteStatementState string

const (
	DataWriteAcknowledged DataWriteStatementState = "acknowledged"
	DataWriteFailed       DataWriteStatementState = "failed"
	DataWriteUnknown      DataWriteStatementState = "unknown"
	DataWriteNotSent      DataWriteStatementState = "not_sent"
)

// DataWriteStatementReceipt describes one original statement for one edit.
// Destination may be a qualified native child of the original document object.
// AffectedRows is the native count, not an UPDATE, concurrency or commit claim.
// NativeJSON retains the original complete response independently of previews.
type DataWriteStatementReceipt struct {
	EditIndex    int
	Kind         string
	Destination  ObjectRef
	State        DataWriteStatementState
	AffectedRows int64
	NativeJSON   string
}

// DataSequentialWriteReceipt records the original ordered batch, including
// unsent suffixes after failure or loss of acknowledgement. Later observations
// cannot turn an unknown statement into an acknowledged one.
type DataSequentialWriteReceipt struct {
	Object     ObjectRef
	Statements []DataWriteStatementReceipt
}

type DataSequentialWriteProgress struct {
	Acknowledged int
	AffectedRows int64
	Complete     bool
	Unknown      bool
}

// SequentialDataWriteProgress validates the correspondence between the original
// statement prefix and the result. It never infers acknowledgement from values,
// affected-row counts, Verified, or a fabricated transaction commit.
func SequentialDataWriteProgress(r *DataEditResult, expected int) (DataSequentialWriteProgress, bool) {
	var p DataSequentialWriteProgress
	if r == nil || r.Sequential == nil || expected <= 0 || r.CommitConfirmed || r.Verified || r.Async != nil || !json.Valid([]byte(r.NativeJSON)) {
		return p, false
	}
	s := r.Sequential
	if s.Object.ProfileID <= 0 || s.Object.Kind == "" || s.Object.Name == "" || len(s.Statements) != expected {
		return p, false
	}
	stopped := false
	for i, statement := range s.Statements {
		if statement.EditIndex != i || statement.Kind == "" || statement.Destination.Namespace() != s.Object.Namespace() || statement.Destination.Kind == "" || statement.Destination.Name == "" || statement.AffectedRows < 0 {
			return DataSequentialWriteProgress{}, false
		}
		if stopped && statement.State != DataWriteNotSent {
			return DataSequentialWriteProgress{}, false
		}
		switch statement.State {
		case DataWriteAcknowledged:
			if !json.Valid([]byte(statement.NativeJSON)) || statement.AffectedRows > math.MaxInt64-p.AffectedRows {
				return DataSequentialWriteProgress{}, false
			}
			p.Acknowledged++
			p.AffectedRows += statement.AffectedRows
		case DataWriteFailed:
			if statement.AffectedRows != 0 || !json.Valid([]byte(statement.NativeJSON)) {
				return DataSequentialWriteProgress{}, false
			}
			stopped = true
		case DataWriteUnknown:
			if statement.AffectedRows != 0 || statement.NativeJSON != "" && !json.Valid([]byte(statement.NativeJSON)) {
				return DataSequentialWriteProgress{}, false
			}
			p.Unknown, stopped = true, true
		case DataWriteNotSent:
			if statement.AffectedRows != 0 || statement.NativeJSON != "" {
				return DataSequentialWriteProgress{}, false
			}
			stopped = true
		default:
			return DataSequentialWriteProgress{}, false
		}
	}
	if r.Applied != p.Acknowledged || len(r.Outcomes) != p.Acknowledged {
		return DataSequentialWriteProgress{}, false
	}
	for i, outcome := range r.Outcomes {
		if outcome.Kind != s.Statements[i].Kind {
			return DataSequentialWriteProgress{}, false
		}
	}
	p.Complete = p.Acknowledged == expected
	return p, true
}

// DataWriteComplete distinguishes an actual transaction confirmation from a
// complete independent statement batch. Callers still validate original edits,
// qualified row identities and document ownership before accepting changes.
func DataWriteComplete(r *DataEditResult, expected int) bool {
	if r == nil || r.CommitUncertain || r.Incomplete {
		return false
	}
	if r.Sequential == nil {
		return r.CommitConfirmed
	}
	p, valid := SequentialDataWriteProgress(r, expected)
	return valid && p.Complete
}

func CloneDataSequentialWriteReceipt(s *DataSequentialWriteReceipt) *DataSequentialWriteReceipt {
	if s == nil {
		return nil
	}
	out := *s
	out.Statements = slices.Clone(s.Statements)
	return &out
}

// NativeWriteReceiptPresentation supplies localized text keys for the original
// receipt view. Nil preserves the existing transaction presentation.
type NativeWriteReceiptPresentation struct {
	Title, Hint, JSONLabel, CloseHint, ExportFilename string
}
