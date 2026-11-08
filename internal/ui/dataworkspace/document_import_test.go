package dataworkspace

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
)

func documentImportTestState(t *testing.T) (*State, *Document, *Session, *testDocumentLease) {
	t.Helper()
	active := &Session{ProfileID: 12, Generation: 3, Connected: true}
	_, cancel := context.WithCancel(context.Background())
	l := &testDocumentLease{cancel: cancel, runtime: Runtime{Capabilities: contract.Capabilities{Documents: true, Import: true}, AsyncWrites: true, DocumentImport: true}}
	s := New(Dependencies{Session: func() Session { return *active }, Runtime: func() Runtime {
		return Runtime{ReserveDocument: func(contract.ObjectRef) (contract.DataDocumentLease, error) { return l, nil }}
	}})
	// Bind before installing callbacks: the same lease runtime value is read by
	// the binding. Never replace it with a current-profile manager.
	object := contract.NamespaceRef{ProfileID: 12, Catalog: "owned"}.Object("documents", "documents")
	d := s.OpenObject(1, object, l.runtime.Capabilities)
	t.Cleanup(func() { s.CloseAll() })
	d.page = &contract.DataPage{Columns: []string{"document"}}
	return s, d, active, l
}

func settleDocumentTransfer(t *testing.T, s *State, d *Document) {
	t.Helper()
	select {
	case r := <-d.transfer.Results:
		ch := make(chan mysqlDataTransferResult, 1)
		ch <- r
		d.transfer.Results = ch
	case <-time.After(3 * time.Second):
		t.Fatal("transfer not acknowledged")
	}
	s.consumeDataTransfers(ui.Services{})
}

func TestDocumentImportParkedAdmissionObservationAndSourceRetention(t *testing.T) {
	s, d, active, _ := documentImportTestState(t)
	var imports, observations atomic.Int32
	entered, release := make(chan struct{}), make(chan struct{})
	result := &contract.DataEditResult{Async: &contract.DataWriteObservation{TaskID: "42", Token: "original", Status: "enqueued", ReceiptJSON: `{"taskUid":42}`}}
	d.boundRuntime.Importer = func(_ context.Context, id int64, db, table string, r contract.DataImportRequest) (*contract.DataImportResult, error) {
		imports.Add(1)
		close(entered)
		<-release
		if id != 12 || db != "owned" || table != "documents" || r.Mode != "document_replace" || r.Edits[0].Values["document"].Text != `{"id":9007199254740993,"nested":{"x":true}}` {
			return nil, errors.New("original immutable import lost")
		}
		return &contract.DataImportResult{Saved: cloneEditResult(result)}, nil
	}
	d.boundRuntime.Verify = func(_ context.Context, _ int64, _, _ string, r *contract.DataEditResult) (*contract.DataVerification, error) {
		observations.Add(1)
		out := cloneEditResult(r)
		if observations.Load() == 1 {
			out.Async.Status = "processing"
			return &contract.DataVerification{Status: "pending", Recovered: out}, nil
		}
		out.Async.Status = "succeeded"
		out.CommitConfirmed = true
		out.Verified = true
		out.Applied = 1
		return &contract.DataVerification{Status: "applied", Recovered: out}, nil
	}
	s.openDataImport(d)
	input, err := parseDocumentImport(strings.NewReader(`{"id":9007199254740993,"nested":{"x":true}}`), "JSON")
	if err != nil {
		t.Fatal(err)
	}
	d.transfer.Input, d.transfer.TotalRows, d.transfer.Confirmation = input, 1, "owned"
	s.startDataImport(ui.Services{}, d)
	<-entered
	active.ProfileID = 13
	close(release)
	settleDocumentTransfer(t, s, d)
	if !d.transfer.Busy || d.edit.Uncertain != nil || active.ProfileID != 13 {
		t.Fatal("parked admission consumed")
	}
	active.ProfileID = 12
	s.consumeDataTransfers(ui.Services{})
	if d.transfer.Completed || !d.transfer.Uncertain || d.edit.Uncertain == nil || d.transfer.Input != input || observations.Load() != 0 {
		t.Fatal("admission confirmed or source discarded")
	}
	s.stopDataTransfer(d)
	if d.transfer.Open || !d.transfer.Uncertain || d.transfer.Input != input {
		t.Fatal("closing wizard lost original task")
	}
	s.VerifySave(ui.Services{}, d)
	ui.NewTester(func(c *ui.Context) { s.transferModals(c, d) }, 720, 520)
	if d.transfer.Canceling {
		t.Fatal("hidden wizard canceled explicit toolbar observation")
	}
	settleDocumentTransfer(t, s, d)
	if !d.transfer.Uncertain || d.edit.Uncertain.Async.Status != "processing" || imports.Load() != 1 {
		t.Fatal("pending task replayed")
	}
	s.VerifySave(ui.Services{}, d)
	settleDocumentTransfer(t, s, d)
	if !d.transfer.Completed || d.transfer.Uncertain || d.edit.Uncertain != nil || d.transfer.Input != input || imports.Load() != 1 {
		t.Fatal("readback not settled")
	}
}

func TestDocumentImportLostReceiptAndStaleSuccessNeverReplay(t *testing.T) {
	for _, mode := range []string{"lost", "stale", "failed", "incomplete", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			s, d, active, _ := documentImportTestState(t)
			var calls atomic.Int32
			entered := make(chan struct{})
			d.boundRuntime.Importer = func(ctx context.Context, _ int64, _, _ string, r contract.DataImportRequest) (*contract.DataImportResult, error) {
				calls.Add(1)
				close(entered)
				if mode == "cancel" {
					<-ctx.Done()
					return &contract.DataImportResult{Saved: &contract.DataEditResult{CommitUncertain: true}}, ctx.Err()
				}
				if mode == "lost" {
					return &contract.DataImportResult{Saved: &contract.DataEditResult{CommitUncertain: true}}, errors.New("lost original response")
				}
				return &contract.DataImportResult{Saved: &contract.DataEditResult{Async: &contract.DataWriteObservation{TaskID: "42", Token: "original", Status: "enqueued"}}}, nil
			}
			d.boundRuntime.Verify = func(_ context.Context, _ int64, _, _ string, observed *contract.DataEditResult) (*contract.DataVerification, error) {
				r := cloneEditResult(observed)
				r.Async.Status = "succeeded"
				r.CommitConfirmed = true
				r.Verified = true
				r.Applied = 1
				if mode == "failed" {
					r.Async.Status = "failed"
					return &contract.DataVerification{Status: "indeterminate", Recovered: r}, nil
				}
				if mode == "incomplete" {
					r.Applied = 0
				}
				return &contract.DataVerification{Status: "applied", Recovered: r}, nil
			}
			s.openDataImport(d)
			d.transfer.Input, _ = parseDocumentImport(strings.NewReader(`{"id":"one"}`), "JSON")
			d.transfer.TotalRows = 1
			d.transfer.Confirmation = "owned"
			s.startDataImport(ui.Services{}, d)
			<-entered
			if mode == "cancel" {
				s.stopDataTransfer(d)
				if !d.transfer.Canceling || !d.transfer.Busy {
					t.Fatal("cancel did not wait")
				}
			}
			settleDocumentTransfer(t, s, d)
			if mode != "lost" && mode != "cancel" {
				s.VerifySave(ui.Services{}, d)
				if mode == "stale" {
					active.Generation++
				}
				settleDocumentTransfer(t, s, d)
			}
			if d.transfer.Completed || !d.transfer.Uncertain || d.edit.Uncertain == nil || d.transfer.Input == nil {
				t.Fatal("uncertain source/task lost")
			}
			s.startDataImport(ui.Services{}, d)
			if calls.Load() != 1 {
				t.Fatal("import replayed")
			}
		})
	}
}
