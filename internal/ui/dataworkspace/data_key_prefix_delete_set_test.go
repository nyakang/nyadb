package dataworkspace

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
)

type prefixSetUIFixture struct {
	mu                          sync.Mutex
	captures, submits, previews atomic.Int32
	set                         *prefixSetUIHandle
	fault                       string
	beforeCapture, beforeSubmit func(context.Context)
	mutate                      func(*contract.DataMutationSnapshot)
}
type prefixSetUIHandle struct {
	mu       sync.Mutex
	f        *prefixSetUIFixture
	snapshot contract.DataKeyPrefixDeleteSetSnapshot
	closed   atomic.Int32
}

func (h *prefixSetUIHandle) Close() error { h.closed.Add(1); return nil }
func (h *prefixSetUIHandle) Snapshot() contract.DataKeyPrefixDeleteSetSnapshot {
	h.mu.Lock()
	defer h.mu.Unlock()
	return contract.CloneDataKeyPrefixDeleteSetSnapshot(h.snapshot)
}
func (h *prefixSetUIHandle) PreviewBatch(ctx context.Context, index int) (contract.DataMutationPlan, error) {
	h.f.previews.Add(1)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	snap := h.Snapshot()
	if snap.Uncertain {
		return nil, contract.ErrDataCommitUncertain
	}
	if snap.Stopped || index != snap.NextBatch {
		return nil, contract.ErrDataConflict
	}
	start := index * snap.BatchSize
	end := min(start+snap.BatchSize, len(snap.Candidates))
	if start >= end {
		return nil, contract.ErrDataConflict
	}
	request := snap.Request
	batch := contract.DataKeyPrefixDeleteBatch{Request: request, Index: index, Offset: start, Count: end - start, Total: len(snap.Candidates)}
	plan := contract.DataMutationSnapshot{Object: request.Object, KeyPrefixDelete: &request, KeyPrefixDeleteBatch: &batch, NativeJSON: fmt.Sprintf(`{"original-batch":%d}`, index)}
	for _, original := range snap.Candidates[start:end] {
		plan.Edits = append(plan.Edits, contract.DataEdit{Kind: "delete", Original: cloneSnapshot(original)})
		plan.Items = append(plan.Items, contract.DataMutationItem{Kind: "delete", EntityID: original.Ref.TypedValues[0].Text})
	}
	if h.f.mutate != nil {
		h.f.mutate(&plan)
	}
	return &mutationTestPlan{snapshot: plan}, nil
}
func prefixSetUIWorkspace(t *testing.T, mode string) (*State, *Document, *Session, *prefixSetUIFixture) {
	t.Helper()
	s, d, active, _ := prefixUIWorkspace(t, mode)
	f := &prefixSetUIFixture{}
	originalSession := *active
	s.deps.SessionFor = func(id int64) Session {
		if active.ProfileID == id {
			return *active
		}
		return originalSession
	}
	template := cloneSnapshot(d.page.RowSnapshots[0])
	template.Ref.Columns[0] = "_original_signed_key"
	object, id, db, table := d.object, d.profileID, d.database, d.table
	d.boundRuntime.CaptureKeyPrefixDelete = func(ctx context.Context, profile int64, database, tableName string, request contract.DataKeyPrefixDelete) (contract.DataKeyPrefixDeleteSet, error) {
		f.captures.Add(1)
		if request.Object != object || profile != id || database != db || tableName != table {
			t.Error("capture changed original document scope")
		}
		h := &prefixSetUIHandle{f: f, snapshot: contract.DataKeyPrefixDeleteSetSnapshot{Request: request, BatchSize: 21, NativeJSON: `{"complete-original-capture":"` + strings.Repeat("猫", 30000) + `"}`}}
		for i := 0; i < 43; i++ {
			row := cloneSnapshot(template)
			key := request.Prefix + fmt.Sprintf("%04d//猫", i)
			row.Ref.Values[0] = []byte("original-signed:" + key)
			row.Ref.TypedValues[0].Text = key
			if mode == "values" {
				row.Original[0].Text = key
			}
			h.snapshot.Candidates = append(h.snapshot.Candidates, row)
		}
		f.mu.Lock()
		f.set = h
		f.mu.Unlock()
		if f.beforeCapture != nil {
			f.beforeCapture(ctx)
		}
		return h, ctx.Err()
	}
	d.boundRuntime.SubmitEdits = func(ctx context.Context, profile int64, database, tableName string, plan contract.DataMutationPlan) (*contract.DataEditResult, error) {
		f.submits.Add(1)
		if profile != id || database != db || tableName != table {
			t.Error("submit changed original document scope")
		}
		snap := plan.Snapshot()
		f.mu.Lock()
		h := f.set
		f.mu.Unlock()
		h.mu.Lock()
		h.snapshot.InFlight = true
		h.mu.Unlock()
		if f.beforeSubmit != nil {
			f.beforeSubmit(ctx)
		}
		receipt := fmt.Sprintf(`{"actual-original-batch-receipt":%d}`, snap.KeyPrefixDeleteBatch.Index)
		out := &contract.DataEditResult{NativeJSON: receipt, Applied: len(snap.Edits), CommitConfirmed: true}
		for _, e := range snap.Edits {
			out.Outcomes = append(out.Outcomes, contract.DataEditOutcome{Kind: e.Kind, Original: cloneSnapshot(e.Original)})
		}
		h.mu.Lock()
		defer h.mu.Unlock()
		h.snapshot.InFlight = false
		h.snapshot.Receipts = append(h.snapshot.Receipts, receipt)
		switch f.fault {
		case "conflict":
			h.snapshot.Stopped = true
			return &contract.DataEditResult{NativeJSON: receipt}, contract.ErrDataConflict
		case "lost":
			h.snapshot.Uncertain = true
			out.CommitConfirmed = false
			out.CommitUncertain = true
			return out, contract.ErrDataCommitUncertain
		case "nil-unknown":
			h.snapshot.Uncertain = true
			h.snapshot.Receipts = h.snapshot.Receipts[:len(h.snapshot.Receipts)-1]
			return nil, contract.ErrDataCommitUncertain
		case "missing":
			out.Outcomes = nil
		case "foreign":
			out.Outcomes[0].Original.Ref.Object.ProfileID++
		case "mismatched-receipt":
			h.snapshot.Receipts[len(h.snapshot.Receipts)-1] = "foreign"
		}
		h.snapshot.NextBatch++
		return out, nil
	}
	return s, d, active, f
}
func waitPrefixSetUI(t *testing.T, s *State, d *Document) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for (d.prefixSet != nil && d.prefixSet.pending()) || d.loading {
		s.Consume(ui.Services{})
		if time.Now().After(deadline) {
			t.Fatal("original set acknowledgement stalled", d.edit.Err)
		}
		time.Sleep(time.Millisecond)
	}
}
func capturePrefixSetUI(t *testing.T, s *State, d *Document) {
	t.Helper()
	s.DeleteKeyPrefixSet(d)
	if d.prefixSet == nil {
		t.Fatal("capture form unavailable")
	}
	d.prefixSet.prefix = "scope/"
	s.captureKeyPrefixSet(ui.Services{}, d)
	waitPrefixSetUI(t, s, d)
	if d.prefixSet == nil || !d.prefixSet.reviewOpen {
		t.Fatal("complete capture absent", d.edit.Err)
	}
}
func previewPrefixSetUI(t *testing.T, s *State, d *Document) {
	t.Helper()
	s.previewKeyPrefixSetBatch(ui.Services{}, d)
	waitPrefixSetUI(t, s, d)
	if d.prefixSet == nil || !d.prefixSet.confirmOpen {
		t.Fatal("original batch confirmation absent", d.edit.Err)
	}
}
func TestDataPrefixSetCompleteCaptureExplicitBatchesRefreshAndIndependentLedger(t *testing.T) {
	for _, mode := range []string{"values", "fingerprints"} {
		t.Run(mode, func(t *testing.T) {
			s, d, _, f := prefixSetUIWorkspace(t, mode)
			before := captureDraft(d)
			s.DeleteKeyPrefixSet(d)
			if !d.HasDraft() || !d.LeavePending() || !s.GuardLeave(d, nil) || s.CanSave(d) || f.captures.Load() != 0 {
				t.Fatal("form dispatched I/O or lost original guards")
			}
			d.prefixSet.prefix = "scope/"
			s.captureKeyPrefixSet(ui.Services{}, d)
			waitPrefixSetUI(t, s, d)
			if !reflect.DeepEqual(before, captureDraft(d)) || len(d.prefixSet.initial.Candidates) != 43 || f.submits.Load() != 0 || f.previews.Load() != 0 {
				t.Fatal("capture narrowed to current page or submitted automatically")
			}
			original := d.prefixSetObservation.JSON
			initialPage := d.page
			for index, count := range []int{21, 21, 1} {
				previewPrefixSetUI(t, s, d)
				if d.prefixSet.preview.KeyPrefixDeleteBatch.Index != index || len(d.prefixSet.preview.Edits) != count {
					t.Fatal("batch lost immutable complete-set scope")
				}
				if f.submits.Load() != int32(index) {
					t.Fatal("preview implicitly submitted a batch")
				}
				s.confirmKeyPrefixSetBatch(ui.Services{}, d)
				s.confirmKeyPrefixSetBatch(ui.Services{}, d)
				waitPrefixSetUI(t, s, d)
				if f.submits.Load() != int32(index+1) || d.prefixSetObservation.JSON != original || len(d.prefixSetProgress.Batches) != index+1 {
					t.Fatal("original evidence overwritten or next batch automatically executed")
				}
				for i, record := range d.prefixSetProgress.Batches {
					if record.Index != i || record.Outcome != "confirmed" || !strings.Contains(record.NativeJSON, fmt.Sprintf(`:%d}`, i)) {
						t.Fatal("complete independent original receipt ledger missing")
					}
				}
				if index < 2 && (d.prefixSet == nil || !d.prefixSet.reviewOpen || d.prefixSet.current.NextBatch != index+1 || d.page == initialPage) {
					t.Fatal("verified refresh dropped fixed capture or retained old page")
				}
			}
			if d.prefixSet != nil || d.HasDraft() || d.edit.Busy || d.edit.Uncertain != nil || f.captures.Load() != 1 || f.set.closed.Load() != 1 || !d.prefixSetLedger.Open {
				t.Fatal("final capture did not release ownership and retain full receipts")
			}
		})
	}
}
func TestDataPrefixSetLaterConflictRetainsEarlierReceiptsAndUnstartedRange(t *testing.T) {
	s, d, _, f := prefixSetUIWorkspace(t, "values")
	capturePrefixSetUI(t, s, d)
	previewPrefixSetUI(t, s, d)
	s.confirmKeyPrefixSetBatch(ui.Services{}, d)
	waitPrefixSetUI(t, s, d)
	first := d.prefixSetProgress.Batches[0].NativeJSON
	original := d.prefixSetObservation.JSON
	f.fault = "conflict"
	previewPrefixSetUI(t, s, d)
	s.confirmKeyPrefixSetBatch(ui.Services{}, d)
	waitPrefixSetUI(t, s, d)
	if d.prefixSet != nil || d.edit.Uncertain != nil || d.edit.Busy || !d.prefixSetProgress.Stopped || d.prefixSetProgress.ConfirmedBatches != 1 || len(d.prefixSetProgress.Batches) != 2 || d.prefixSetProgress.Batches[0].NativeJSON != first || d.prefixSetProgress.Batches[1].Outcome != "failed" || d.prefixSetObservation.JSON != original {
		t.Fatal("known later failure hid partial durable progress")
	}
	if !strings.Contains(prefixSetProgressText(d.prefixSetProgress), "1") {
		t.Fatal("unstarted final candidate absent")
	}
	s.confirmKeyPrefixSetBatch(ui.Services{}, d)
	if f.submits.Load() != 2 {
		t.Fatal("stopped batch replayed")
	}
}
func TestDataPrefixSetUnknownIncompleteAndForeignReceiptsBlockReplayAndPreserveCompleteEvidence(t *testing.T) {
	for _, fault := range []string{"lost", "nil-unknown", "missing", "foreign", "mismatched-receipt"} {
		t.Run(fault, func(t *testing.T) {
			s, d, _, f := prefixSetUIWorkspace(t, "fingerprints")
			capturePrefixSetUI(t, s, d)
			previewPrefixSetUI(t, s, d)
			original := d.prefixSetObservation.JSON
			f.fault = fault
			s.confirmKeyPrefixSetBatch(ui.Services{}, d)
			waitPrefixSetUI(t, s, d)
			if d.prefixSet == nil || !d.prefixSet.uncertain || d.edit.Uncertain == nil || d.edit.Busy || d.prefixSetProgress.ConfirmedBatches != 0 || len(d.prefixSetProgress.Batches) != 1 || d.prefixSetProgress.Batches[0].Outcome != "unknown" || d.prefixSetObservation.JSON != original || !s.GuardLeave(d, nil) {
				t.Fatal("unverified batch promoted progress or lost original evidence", d.edit.Err)
			}
			ledger := d.prefixSetLedger.JSON
			s.previewKeyPrefixSetBatch(ui.Services{}, d)
			s.confirmKeyPrefixSetBatch(ui.Services{}, d)
			s.DeleteKeyPrefixSet(d)
			if f.submits.Load() != 1 || f.captures.Load() != 1 || d.prefixSetLedger.JSON != ledger {
				t.Fatal("unknown write authorized another batch, capture, or receipt replacement")
			}
			s.Close(d)
			if d.prefixSet != nil || d.prefixSetObservation.JSON != "" || d.prefixSetLedger.JSON != "" || len(d.prefixSetProgress.Batches) != 0 {
				t.Fatal("closed document retained full capture or ledger caches")
			}
		})
	}
}
func TestDataPrefixSetCanceledBatchConfirmationAndStoppedCaptureNeverSubmit(t *testing.T) {
	s, d, _, f := prefixSetUIWorkspace(t, "values")
	capturePrefixSetUI(t, s, d)
	previewPrefixSetUI(t, s, d)
	original := d.prefixSetObservation.JSON
	plan := d.prefixSet.plan.(*mutationTestPlan)
	s.cancelKeyPrefixSetConfirmation(d)
	if f.submits.Load() != 0 || plan.closes.Load() != 1 || !d.prefixSet.reviewOpen || d.prefixSetObservation.JSON != original {
		t.Fatal("cancel confirmation submitted or discarded complete capture")
	}
	s.stopKeyPrefixSet(d)
	if d.prefixSet != nil || d.edit.Busy || d.HasDraft() || d.prefixSetObservation.JSON != original || !d.prefixSetProgress.Stopped || f.set.closed.Load() != 1 {
		t.Fatal("stopped capture lost evidence or retained authority")
	}
	ledger := d.prefixSetLedger.JSON
	s.DeleteKeyPrefixSet(d)
	s.stopKeyPrefixSet(d)
	if d.prefixSetLedger.JSON != ledger || f.captures.Load() != 1 {
		t.Fatal("canceled new input modified earlier original evidence")
	}
}
func TestDataPrefixSetPlanAndCaptureInspectionCannotReplaceOriginalCandidates(t *testing.T) {
	for _, fault := range []string{"descriptor", "original", "count", "index", "values", "scope"} {
		t.Run(fault, func(t *testing.T) {
			s, d, _, f := prefixSetUIWorkspace(t, "values")
			capturePrefixSetUI(t, s, d)
			f.mutate = func(p *contract.DataMutationSnapshot) {
				switch fault {
				case "descriptor":
					p.KeyPrefixDeleteBatch = nil
				case "original":
					p.Edits[0].Original.Ref.TypedValues[0].Text = "foreign"
				case "count":
					p.KeyPrefixDeleteBatch.Count--
				case "index":
					p.KeyPrefixDeleteBatch.Index++
				case "values":
					p.Edits[0].Values = map[string]contract.DataValue{"value": {Kind: contract.Text, Text: "foreign"}}
				case "scope":
					p.KeyPrefixDelete.Prefix = "foreign"
				}
			}
			s.previewKeyPrefixSetBatch(ui.Services{}, d)
			waitPrefixSetUI(t, s, d)
			if d.prefixSet == nil || d.prefixSet.confirmOpen || d.prefixSet.plan != nil || f.submits.Load() != 0 || d.edit.Err == "" {
				t.Fatal("malformed original plan admitted")
			}
		})
	}
}
func TestDataPrefixSetParkedOriginalCompletionsAndGenerationReplacementNeverUseFreshExecutor(t *testing.T) {
	for _, phase := range []string{"capture", "submit"} {
		for _, mode := range []string{"parked", "replace"} {
			t.Run(phase+"/"+mode, func(t *testing.T) {
				s, d, active, f := prefixSetUIWorkspace(t, "values")
				if phase == "submit" {
					capturePrefixSetUI(t, s, d)
					previewPrefixSetUI(t, s, d)
					s.confirmKeyPrefixSetBatch(ui.Services{}, d)
				} else {
					s.DeleteKeyPrefixSet(d)
					d.prefixSet.prefix = "scope/"
					s.captureKeyPrefixSet(ui.Services{}, d)
				}
				m := d.prefixSet
				if phase == "capture" {
					select {
					case err := <-m.captureResults:
						ch := make(chan error, 1)
						ch <- err
						m.captureResults = ch
					case <-time.After(5 * time.Second):
						t.Fatal("capture ACK stalled")
					}
				} else {
					select {
					case r := <-m.results:
						ch := make(chan dataPrefixSetCompletion, 1)
						ch <- r
						m.results = ch
					case <-time.After(5 * time.Second):
						t.Fatal("submit ACK stalled")
					}
				}
				if mode == "parked" {
					active.ProfileID++
					s.Consume(ui.Services{})
					if !m.pending() || !d.edit.Busy {
						t.Fatal("foreign profile consumed original completion")
					}
					active.ProfileID--
				} else {
					active.Generation++
				}
				waitPrefixSetUI(t, s, d)
				if mode == "parked" {
					if d.prefixSet == nil || !d.prefixSet.reviewOpen {
						t.Fatal("original activation lost parked state")
					}
				} else if phase == "capture" {
					if d.prefixSet != nil || f.set.closed.Load() != 1 {
						t.Fatal("late capture kept authority")
					}
				} else {
					if d.edit.Uncertain == nil || !d.prefixSet.uncertain || d.prefixSetProgress.ConfirmedBatches != 0 {
						t.Fatal("late generation promoted old batch")
					}
				}
				if f.captures.Load() != 1 {
					t.Fatal("completion acquired a new executor")
				}
			})
		}
	}
}
func TestDataPrefixSetCancellationWaitsOriginalAcknowledgement(t *testing.T) {
	s, d, _, f := prefixSetUIWorkspace(t, "values")
	capturePrefixSetUI(t, s, d)
	previewPrefixSetUI(t, s, d)
	entered, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	f.beforeSubmit = func(ctx context.Context) { close(entered); <-ctx.Done(); close(canceled); <-release }
	f.fault = "lost"
	s.confirmKeyPrefixSetBatch(ui.Services{}, d)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("original executor did not enter")
	}
	s.stopKeyPrefixSet(d)
	select {
	case <-canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("original executor did not observe cancellation")
	}
	s.Consume(ui.Services{})
	if !d.edit.Busy || !d.prefixSet.pending() {
		t.Fatal("cancel settled before acknowledgement")
	}
	close(release)
	waitPrefixSetUI(t, s, d)
	if d.edit.Busy || d.edit.Uncertain == nil || !d.prefixSet.uncertain || f.submits.Load() != 1 {
		t.Fatal("processed cancellation lost original uncertainty")
	}
}
func TestDataPrefixSetEvidenceExportsCaptureImmutableOriginalFiles(t *testing.T) {
	s, d, _, _ := prefixSetUIWorkspace(t, "values")
	capturePrefixSetUI(t, s, d)
	previewPrefixSetUI(t, s, d)
	s.confirmKeyPrefixSetBatch(ui.Services{}, d)
	waitPrefixSetUI(t, s, d)
	for _, evidence := range []*nativeWriteReceiptState{&d.prefixSetObservation, &d.prefixSetLedger} {
		original := evidence.JSON
		entered, release := make(chan struct{}), make(chan struct{})
		target := filepath.Join(t.TempDir(), "original.json")
		s.exportDataNativeEvidence(ui.Services{}, d, evidence, func(mygo.SaveDialogOptions) (string, error) { close(entered); <-release; return target, nil }, "导出完整内容", "original.json")
		<-entered
		evidence.JSON = "replacement"
		close(release)
		select {
		case err := <-evidence.Results:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("export stalled")
		}
		evidence.Cancel()
		evidence.Cancel = nil
		evidence.Results = nil
		b, err := os.ReadFile(target)
		if err != nil || string(b) != original {
			t.Fatal("picker delay exported replacement evidence", err)
		}
	}
}
func TestDataPrefixSetValidLongTypedCandidateKeys(t *testing.T) {
	s, d, _, f := prefixSetUIWorkspace(t, "values")
	capturePrefixSetUI(t, s, d)
	row := cloneSnapshot(d.prefixSet.initial.Candidates[0])
	row.Ref.TypedValues[0].Text = "scope/" + strings.Repeat("x", 6000)
	capture := contract.CloneDataKeyPrefixDeleteSetSnapshot(d.prefixSet.initial)
	capture.Candidates[0] = row
	if !validPrefixSetCapture(capture.Request, capture) {
		t.Fatal("valid long original key narrowed to prefix input bound")
	}
	if f.captures.Load() != 1 {
		t.Fatal("validation read again")
	}
}

func TestDataPrefixSetDocumentCloseWaitsOriginalCaptureAndSubmitAcknowledgement(t *testing.T) {
	for _, phase := range []string{"capture", "submit"} {
		t.Run(phase, func(t *testing.T) {
			s, d, _, f := prefixSetUIWorkspace(t, "fingerprints")
			entered, canceled, release, closed := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
			wait := func(ctx context.Context) { close(entered); <-ctx.Done(); close(canceled); <-release }
			if phase == "capture" {
				f.beforeCapture = wait
				s.DeleteKeyPrefixSet(d)
				d.prefixSet.prefix = "scope/"
				s.captureKeyPrefixSet(ui.Services{}, d)
			} else {
				capturePrefixSetUI(t, s, d)
				previewPrefixSetUI(t, s, d)
				f.beforeSubmit = wait
				f.fault = "lost"
				s.confirmKeyPrefixSetBatch(ui.Services{}, d)
			}
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("original executor did not enter")
			}
			// This goroutine is the sole state owner until Close acknowledges completion.
			go func() { s.Close(d); close(closed) }()
			select {
			case <-canceled:
			case <-time.After(5 * time.Second):
				t.Fatal("original executor cancellation not observed")
			}
			select {
			case <-closed:
				t.Fatal("document close returned before original executor ACK")
			default:
			}
			close(release)
			select {
			case <-closed:
			case <-time.After(5 * time.Second):
				t.Fatal("original close did not settle after ACK")
			}
			if !d.closed || d.prefixSet != nil || d.prefixSetLedger.JSON != "" || d.prefixSetObservation.JSON != "" || f.set.closed.Load() != 1 {
				t.Fatal("closed document retained original caches or late authority")
			}
			s.Close(d)
			if f.set.closed.Load() != 1 {
				t.Fatal("idempotent close released the original handle twice")
			}
		})
	}
}

func TestDataPrefixSetNewlineCandidateListHasUnambiguousOriginalJSON(t *testing.T) {
	s, d, _, _ := prefixSetUIWorkspace(t, "fingerprints")
	capturePrefixSetUI(t, s, d)
	var keys []string
	if json.Unmarshal([]byte(d.prefixSet.keysJSON), &keys) != nil || len(keys) != 43 {
		t.Fatal("complete candidate clipboard omitted keys")
	}
	initial := contract.CloneDataKeyPrefixDeleteSetSnapshot(d.prefixSet.initial)
	initial.Candidates[0].Ref.TypedValues[0].Text = "scope/line\nbreak"
	if !validPrefixSetCapture(initial.Request, initial) {
		t.Fatal("native newline key rejected")
	}
}

func TestDataPrefixSetSubmissionKeepsApprovedOriginalBatchAcrossLaterUIPreviewChanges(t *testing.T) {
	s, d, _, f := prefixSetUIWorkspace(t, "fingerprints")
	capturePrefixSetUI(t, s, d)
	previewPrefixSetUI(t, s, d)
	entered, release := make(chan struct{}), make(chan struct{})
	f.beforeSubmit = func(context.Context) { close(entered); <-release }
	s.confirmKeyPrefixSetBatch(ui.Services{}, d)
	<-entered
	d.prefixSet.preview.KeyPrefixDeleteBatch.Index = 99
	d.prefixSet.preview.Edits[0].Original.Ref.TypedValues[0].Text = "later-ui-change"
	close(release)
	waitPrefixSetUI(t, s, d)
	if d.edit.Uncertain != nil || d.prefixSet == nil || d.prefixSet.current.NextBatch != 1 || len(d.prefixSetProgress.Batches) != 1 || d.prefixSetProgress.Batches[0].Index != 0 || d.prefixSetProgress.Batches[0].Count != 21 || d.prefixSetProgress.Batches[0].Outcome != "confirmed" {
		t.Fatal("completion used later mutable UI preview", d.edit.Err)
	}
}
