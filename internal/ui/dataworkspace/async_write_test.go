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

func TestAsyncDataWriteRetainsDraftParkedAdmissionExplicitObservationAndGuards(t *testing.T) {
	active := Session{ProfileID: 12, Generation: 3, Connected: true}
	original := active
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	l := &testDocumentLease{cancel: cancel}
	var saves, observes, reads atomic.Int32
	entered, release := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	object := contract.NamespaceRef{ProfileID: 12, Catalog: "owned"}.Object("documents", "documents")
	snapshot := &contract.RowSnapshot{Mode: "fingerprints", Columns: []string{"document"}, Ref: contract.RowRef{Object: object, Database: "owned", Table: "documents", Columns: []string{"id"}, Values: [][]byte{[]byte("one")}}}
	result := &contract.DataEditResult{Async: &contract.DataWriteObservation{Token: "original", TaskID: "42", Status: "enqueued", ReceiptJSON: `{"taskUid":42}`}}
	l.runtime = Runtime{Capabilities: contract.Capabilities{Documents: true, EditableTables: true}, AsyncWrites: true, WriteDescription: "POST /indexes/owned/documents", Saver: func(_ context.Context, _ int64, _, _ string, edits []contract.DataEdit) (*contract.DataEditResult, error) {
		saves.Add(1)
		close(entered)
		<-release
		if len(edits) != 1 || edits[0].Values["document"].Text != `{"id":"one","n":9007199254740993}` {
			return nil, errors.New("write copy lost")
		}
		return cloneEditResult(result), nil
	}, Verify: func(_ context.Context, _ int64, _, _ string, r *contract.DataEditResult) (*contract.DataVerification, error) {
		if r.Async.Token != "original" {
			return nil, errors.New("original observation missing")
		}
		observes.Add(1)
		out := cloneEditResult(r)
		if observes.Load() == 1 {
			out.Async.Status = "processing"
			return &contract.DataVerification{Status: "pending", Recovered: out}, nil
		}
		out.Async.Status = "succeeded"
		out.Async.TaskJSON = `{"uid":42,"status":"succeeded"}`
		out.CommitConfirmed = true
		out.Applied = 1
		out.Outcomes = []contract.DataEditOutcome{{Kind: "update", Original: snapshot, Saved: snapshot, PreviewColumns: []string{"document"}, Previews: []contract.DataCell{{Text: `{"id":"one","n":9007199254740993}`}}}}
		return &contract.DataVerification{Status: "applied", Recovered: out}, nil
	}, AdvancedLoader: func(context.Context, int64, string, string, contract.DataOptions) (*contract.DataPage, error) {
		reads.Add(1)
		return nil, errors.New("unexpected refresh")
	}}
	s := New(Dependencies{Session: func() Session { return active }, SessionFor: func(int64) Session { return original }, Runtime: func() Runtime {
		return Runtime{ReserveDocument: func(contract.ObjectRef) (contract.DataDocumentLease, error) { return l, nil }}
	}})
	d := s.OpenObject(1, object, l.runtime.Capabilities)
	defer s.Close(d)
	d.page = &contract.DataPage{Columns: []string{"document"}, ColumnMetadata: []contract.StructureColumn{{Name: "document", DataType: "json", ValueKind: contract.JSON}}, Rows: [][]contract.DataCell{{{Text: `{"id":"one"}`}}}, RowRefs: []*contract.RowRef{&snapshot.Ref}, RowSnapshots: []*contract.RowSnapshot{snapshot}}
	s.EditCell(ui.Services{}, d, 0, "document", contract.Cell{Kind: contract.JSON, Text: `{"id":"one","n":9007199254740993}`})
	if !d.edit.Batch || !s.CanSave(d) {
		t.Fatal("async grid not explicit batch")
	}
	continued := false
	d.edit.AfterSave = func(ui.Services) { continued = true }
	s.Save(ui.Services{}, d)
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("write not entered")
	}
	active.ProfileID = 13
	close(release)
	settle := func() {
		t.Helper()
		select {
		case r := <-d.edit.Results:
			ch := make(chan mysqlEditResult, 1)
			ch <- r
			d.edit.Results = ch
		case <-time.After(time.Second):
			t.Fatal("write did not acknowledge")
		}
	}
	settle()
	s.Consume(ui.Services{})
	if !d.edit.Busy || continued || active.ProfileID != 13 {
		t.Fatal("parked admission changed active profile")
	}
	active.ProfileID = 12
	s.Consume(ui.Services{})
	if d.edit.Uncertain == nil || d.edit.Uncertain.CommitUncertain || !d.HasDraft() || s.CanSave(d) || continued || observes.Load() != 0 {
		t.Fatal("admission cleared/replayed draft or ran continuation")
	}
	page := d.page
	s.Load(ui.Services{}, d, true)
	s.discardMySQLData(d)
	s.Save(ui.Services{}, d)
	if d.page != page || reads.Load() != 0 || saves.Load() != 1 || !s.GuardLeave(d, nil) {
		t.Fatal("pending write guard bypassed")
	}
	s.VerifySave(ui.Services{}, d)
	settle()
	s.Consume(ui.Services{})
	if d.edit.Uncertain.Async.Status != "processing" || !d.HasDraft() {
		t.Fatal("processing task reported completion")
	}
	s.VerifySave(ui.Services{}, d)
	settle()
	s.Consume(ui.Services{})
	if d.HasDraft() || d.edit.Uncertain != nil || continued || saves.Load() != 1 || observes.Load() != 2 || d.edit.LastAsync.TaskJSON == "" {
		t.Fatal("observed actual outcome not applied")
	}
	if outcome := dataActivityOutcome(nil, result, 1, false); outcome.State != "pending" || outcome.AffectedRows != 0 {
		t.Fatal("admission logged successful affected rows")
	}
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Toolbar(c, func() { s.asyncDataWriteActions(c, d) })
		s.mysqlDataEditStatus(c, d)
		s.Modals(c)
	}, 1280, 800)
	if err := tt.Click("写入任务证据"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("复制写入任务受理"); err != nil {
		t.Fatal(err)
	}
	if tt.Clipboard() != result.Async.ReceiptJSON {
		t.Fatal("original evidence copy missing")
	}
	s.Close(d)
	if d.edit.LastAsync != nil || d.edit.AsyncEvidenceOpen || l.closed.Load() != 1 {
		t.Fatal("closed data document retained async evidence/lease")
	}
}

func TestAsyncDataWriteUnknownReceiptAndGenerationNeverReplay(t *testing.T) {
	for _, mode := range []string{"unknown", "generation"} {
		t.Run(mode, func(t *testing.T) {
			active := Session{ProfileID: 12, Generation: 1, Connected: true}
			var saves atomic.Int32
			r := Runtime{Capabilities: contract.Capabilities{EditableTables: true, Documents: true}, AsyncWrites: true, Saver: func(context.Context, int64, string, string, []contract.DataEdit) (*contract.DataEditResult, error) {
				saves.Add(1)
				if mode == "unknown" {
					return &contract.DataEditResult{CommitUncertain: true}, errors.New("acknowledgment lost")
				}
				return &contract.DataEditResult{Async: &contract.DataWriteObservation{TaskID: "42", Status: "enqueued"}}, nil
			}}
			s := New(Dependencies{Session: func() Session { return active }, Runtime: func() Runtime { return r }})
			d := s.OpenObject(1, contract.NamespaceRef{ProfileID: 12, Catalog: "owned"}.Object("documents", "documents"), r.Capabilities)
			defer s.Close(d)
			d.page = &contract.DataPage{Columns: []string{"document"}, Rows: [][]contract.DataCell{{{Text: "original"}}}}
			d.edit.Drafts = map[string]*mysqlRowDraft{"one": {Key: "one", Row: 0, Kind: "insert", Values: map[string]contract.DataValue{"document": {Kind: contract.JSON, Text: `{"id":"one"}`}}}}
			s.Save(ui.Services{}, d)
			select {
			case saved := <-d.edit.Results:
				ch := make(chan mysqlEditResult, 1)
				ch <- saved
				d.edit.Results = ch
			case <-time.After(time.Second):
				t.Fatal("save stalled")
			}
			if mode == "generation" {
				active.Generation++
			}
			s.Consume(ui.Services{})
			if d.edit.Busy || d.edit.Uncertain == nil || !d.edit.Uncertain.CommitUncertain || !d.HasDraft() {
				t.Fatal("unknown/stale write not settled and retained")
			}
			s.Save(ui.Services{}, d)
			s.Load(ui.Services{}, d, true)
			s.discardMySQLData(d)
			if saves.Load() != 1 || !d.HasDraft() || d.loading {
				t.Fatal("unknown/stale write replayed/discarded")
			}
		})
	}
}

func TestAsyncEvidencePreviewBoundsPreserveCompleteSource(t *testing.T) {
	raw := strings.Repeat("猫🐱", 20000)
	preview := asyncEvidencePreview(raw)
	if len(preview) > 66<<10 || !strings.Contains(preview, "预览已截断") || len(raw) != 140000 {
		t.Fatal("evidence preview unbounded or source modified")
	}
}

func TestAsyncDataCloseRequiresExplicitOriginalCallbackAndKeepsReplayBlocked(t *testing.T) {
	r := Runtime{AsyncWrites: true}
	s := New(Dependencies{Runtime: func() Runtime { return r }})
	d := s.OpenObject(1, contract.NamespaceRef{ProfileID: 12, Catalog: "owned"}.Object("documents", "documents"), contract.Capabilities{})
	defer s.Close(d)
	d.edit.Uncertain = &contract.DataEditResult{CommitUncertain: true}
	d.edit.Err = "acknowledgment lost"
	d.edit.Drafts = map[string]*mysqlRowDraft{"one": {Key: "one", Kind: "insert"}}
	closed := 0
	s.deps.CloseDocument = func(_ ui.Services, original *Document) {
		if original != d || s.GuardLeave(original, nil) {
			t.Fatal("explicit close did not authorize original document")
		}
		closed++
		s.Close(original)
	}
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Toolbar(c, func() { s.asyncDataWriteActions(c, d) })
		s.mysqlDataEditStatus(c, d)
		s.Modals(c)
	}, 1280, 800)
	if err := tt.Click("关闭待核验文档"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("继续观察"); err != nil {
		t.Fatal(err)
	}
	if closed != 0 || !d.HasDraft() || !s.GuardLeave(d, nil) {
		t.Fatal("close cancel discarded/unblocked unknown draft")
	}
	if err := tt.Click("关闭待核验文档"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("关闭并放弃本地草稿与证据"); err != nil {
		t.Fatal(err)
	}
	if closed != 1 || !d.closed || d.edit.Uncertain != nil || len(d.edit.Drafts) != 0 {
		t.Fatal("explicit close did not release local data")
	}
}
