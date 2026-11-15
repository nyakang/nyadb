package contract

import (
	"math"
	"testing"
)

func sequentialReceiptFixture(states ...DataWriteStatementState) *DataEditResult {
	object := ObjectRef{ProfileID: 7, Catalog: "original", Kind: "stable", Name: "observations"}
	r := &DataEditResult{NativeJSON: `{"original_batch":true}`, Sequential: &DataSequentialWriteReceipt{Object: object}}
	for i, state := range states {
		s := DataWriteStatementReceipt{EditIndex: i, Kind: "update", Destination: object.Namespace().Object("table", "child"), State: state}
		if state == DataWriteAcknowledged || state == DataWriteFailed {
			s.NativeJSON = `{"original_response":true}`
		}
		if state == DataWriteAcknowledged {
			s.AffectedRows = int64(i + 1)
			r.Applied++
			r.Outcomes = append(r.Outcomes, DataEditOutcome{Kind: "update"})
		}
		r.Sequential.Statements = append(r.Sequential.Statements, s)
	}
	return r
}

func TestSequentialDataWriteKeepsIndependentAcknowledgementsAndNativeCounts(t *testing.T) {
	for _, test := range []struct {
		name              string
		states            []DataWriteStatementState
		acknowledged      int
		rows              int64
		complete, unknown bool
	}{
		{"complete", []DataWriteStatementState{DataWriteAcknowledged, DataWriteAcknowledged, DataWriteAcknowledged}, 3, 6, true, false},
		{"native-failure", []DataWriteStatementState{DataWriteAcknowledged, DataWriteFailed, DataWriteNotSent}, 1, 1, false, false},
		{"lost-response", []DataWriteStatementState{DataWriteAcknowledged, DataWriteUnknown, DataWriteNotSent}, 1, 1, false, true},
		{"not-dispatched", []DataWriteStatementState{DataWriteNotSent, DataWriteNotSent, DataWriteNotSent}, 0, 0, false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := sequentialReceiptFixture(test.states...)
			p, valid := SequentialDataWriteProgress(r, 3)
			if !valid || p.Acknowledged != test.acknowledged || p.AffectedRows != test.rows || p.Complete != test.complete || p.Unknown != test.unknown || DataWriteComplete(r, 3) != test.complete || r.CommitConfirmed {
				t.Fatalf("lost independent original receipts: %+v valid=%v", p, valid)
			}
			// An accepted zero-row statement remains an acknowledgement. The
			// native row count cannot become a concurrency or UPDATE claim.
			if test.complete {
				r.Sequential.Statements[0].AffectedRows = 0
				if !DataWriteComplete(r, 3) {
					t.Fatal("native zero count revoked an original acknowledgement")
				}
			}
		})
	}
}

func TestSequentialDataWriteRejectsFabricatedCommitRecoveryAndIncoherentReceipts(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*DataEditResult)
	}{
		{"transaction-claim", func(r *DataEditResult) { r.CommitConfirmed = true }},
		{"state-equality-verification", func(r *DataEditResult) { r.Verified = true }},
		{"async-substitute", func(r *DataEditResult) { r.Async = &DataWriteObservation{Token: "later"} }},
		{"missing-complete-receipt", func(r *DataEditResult) { r.NativeJSON = "" }},
		{"missing-native-ack", func(r *DataEditResult) { r.Sequential.Statements[0].NativeJSON = "" }},
		{"invalid-native-ack", func(r *DataEditResult) { r.Sequential.Statements[0].NativeJSON = "{" }},
		{"negative-native-count", func(r *DataEditResult) { r.Sequential.Statements[0].AffectedRows = -1 }},
		{"overflow-native-count", func(r *DataEditResult) { r.Sequential.Statements[0].AffectedRows = math.MaxInt64 }},
		{"foreign-profile", func(r *DataEditResult) { r.Sequential.Statements[0].Destination.ProfileID++ }},
		{"foreign-catalog", func(r *DataEditResult) { r.Sequential.Statements[0].Destination.Catalog = "other" }},
		{"unqualified-child", func(r *DataEditResult) { r.Sequential.Statements[0].Destination.Name = "" }},
		{"wrong-index", func(r *DataEditResult) { r.Sequential.Statements[0].EditIndex++ }},
		{"wrong-outcome-kind", func(r *DataEditResult) { r.Outcomes[0].Kind = "delete" }},
		{"missing-prefix-outcome", func(r *DataEditResult) { r.Outcomes = r.Outcomes[:1] }},
		{"native-count-as-applied-edits", func(r *DataEditResult) { r.Applied = 3 }},
		{"acknowledged-after-failure", func(r *DataEditResult) {
			r.Sequential.Statements[0].State = DataWriteFailed
			r.Sequential.Statements[0].AffectedRows = 0
		}},
		{"unknown-with-count", func(r *DataEditResult) { r.Sequential.Statements[0].State = DataWriteUnknown }},
		{"unsent-with-response", func(r *DataEditResult) {
			r.Sequential.Statements[0].State = DataWriteNotSent
			r.Sequential.Statements[0].AffectedRows = 0
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := sequentialReceiptFixture(DataWriteAcknowledged, DataWriteAcknowledged)
			test.mutate(r)
			if _, valid := SequentialDataWriteProgress(r, 2); valid || DataWriteComplete(r, 2) {
				t.Fatal("unrelated or fabricated evidence authorized native writes")
			}
		})
	}
	for _, flag := range []string{"incomplete", "uncertain"} {
		r := sequentialReceiptFixture(DataWriteAcknowledged)
		r.Incomplete, r.CommitUncertain = flag == "incomplete", flag == "uncertain"
		if DataWriteComplete(r, 1) {
			t.Fatal("unsettled write authorized acceptance", flag)
		}
	}
	if !DataWriteComplete(&DataEditResult{CommitConfirmed: true}, 1) || DataWriteComplete(&DataEditResult{}, 1) {
		t.Fatal("existing transactional confirmation changed")
	}
}

func TestSequentialDataWriteReceiptCloneOwnsOriginalPrefix(t *testing.T) {
	r := sequentialReceiptFixture(DataWriteAcknowledged, DataWriteUnknown)
	copy := CloneDataSequentialWriteReceipt(r.Sequential)
	r.Sequential.Statements[0].NativeJSON = `{"replacement":true}`
	r.Sequential.Statements[1].State = DataWriteAcknowledged
	if copy.Statements[0].NativeJSON != `{"original_response":true}` || copy.Statements[1].State != DataWriteUnknown {
		t.Fatal("later receipt mutated original owned prefix or unknown acknowledgement")
	}
}
