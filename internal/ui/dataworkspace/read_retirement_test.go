package dataworkspace

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestRetiredPageWaitsAcknowledgementKeepsAuxiliaryReadsAndRequiresExplicitReopen(t *testing.T) {
	active := Session{ProfileID: 12, Generation: 4, Connected: true}
	object := contract.NamespaceRef{ProfileID: 12, Catalog: "original"}.Object("label", "same")
	started, canceled, ack := make(chan struct{}), make(chan struct{}), make(chan struct{})
	chunkStarted, countStarted, auxiliaryAck := make(chan struct{}), make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	lease := &testDocumentLease{cancel: cancel}
	var calls atomic.Int32
	lease.runtime = Runtime{PageFailureRequiresReopen: true, Capabilities: contract.Capabilities{Count: true, FieldChunks: true, Keyset: true}, AdvancedLoader: func(read context.Context, _ int64, _, _ string, _ contract.DataOptions) (*contract.DataPage, error) {
		calls.Add(1)
		lease.work.Add(1)
		defer lease.work.Done()
		close(started)
		<-read.Done()
		close(canceled)
		<-ack
		// A driver can deliver a late successful result after cancellation.
		return &contract.DataPage{Object: object, Columns: []string{"late"}}, nil
	}, Count: func(read context.Context, _ int64, _, _ string) (int64, error) {
		close(countStarted)
		select {
		case <-auxiliaryAck:
			return 7, nil
		case <-read.Done():
			return 0, read.Err()
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}, Chunk: func(read context.Context, _ int64, _, _, _ string, ref contract.RowRef, _ int64, _ int) (*contract.FieldChunk, error) {
		close(chunkStarted)
		if ref.Object != object {
			return nil, contract.ErrDataConflict
		}
		select {
		case <-auxiliaryAck:
			return &contract.FieldChunk{Text: "original field", NextOffset: 14, TotalUnits: 14}, nil
		case <-read.Done():
			return nil, read.Err()
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}}
	reopened := 0
	s := New(Dependencies{Session: func() Session { return active }, SessionFor: func(id int64) Session { return Session{ProfileID: id, Generation: active.Generation, Connected: true} }, Runtime: func() Runtime {
		return Runtime{ReserveDocument: func(contract.ObjectRef) (contract.DataDocumentLease, error) { return lease, nil }}
	}, Inspector: InspectorDependencies{Session: func() Session { return active }}, ReopenDocument: func(_ ui.Services, d *Document) {
		if d.Object() != object || d.Generation() != 4 {
			t.Error("reopen lost original identity")
		}
		reopened++
	}})
	d := s.OpenObject(1, object, lease.runtime.Capabilities)
	defer func() {
		select {
		case <-ack:
		default:
			close(ack)
		}
		select {
		case <-auxiliaryAck:
		default:
			close(auxiliaryAck)
		}
		s.Close(d)
	}()
	cached := &contract.DataPage{Object: object, Columns: []string{"properties"}, Rows: [][]contract.DataCell{{{Text: "cached"}}}, RowRefs: []*contract.RowRef{{Object: object}}}
	d.page, d.selectedRow, d.grid.Cells.Cursor = cached, 0, "properties"
	panel := s.inspectorPanel(d)
	s.Load(ui.Services{}, d, true)
	awaitRetirementSignal(t, started)
	s.inspector.LoadCount(panel, nil)
	s.inspector.LoadChunk(panel, nil)
	awaitRetirementSignal(t, countStarted)
	awaitRetirementSignal(t, chunkStarted)
	s.Cancel(d)
	awaitRetirementSignal(t, canceled)
	if !d.loading || !d.readRetired || d.results == nil || !panel.CountLoading() || !panel.ChunkLoading() || s.CanReopen(d) {
		t.Fatal("cancel settled before acknowledgement or canceled auxiliary readers")
	}
	s.Load(ui.Services{}, d, true)
	s.Reopen(ui.Services{}, d)
	if calls.Load() != 1 || reopened != 0 {
		t.Fatal("retired request replayed before acknowledgement")
	}
	close(ack)
	active.ProfileID = 13
	parkRetirementPage(t, d)
	s.Consume(ui.Services{})
	if !d.loading || d.page != cached {
		t.Fatal("parked cancellation changed original document")
	}
	close(auxiliaryAck)
	parkRetirementAuxiliary(t, panel)
	s.Consume(ui.Services{})
	if !panel.CountLoading() || !panel.ChunkLoading() {
		t.Fatal("parked auxiliary completion was consumed")
	}
	active.ProfileID = 12
	s.Consume(ui.Services{})
	if d.loading || d.page != cached || d.err == "" || !d.readRetired || !s.CanReopen(d) || panel.CountError() != "" || panel.ChunkError() != "" || panel.Count() == nil || *panel.Count() != 7 || panel.ChunkText() != "original field" {
		t.Fatal("acknowledgement lost cached page or independent auxiliary completion", d.err, panel.CountError(), panel.ChunkError())
	}
	s.Load(ui.Services{}, d, true)
	if calls.Load() != 1 || d.loading {
		t.Fatal("retired range replayed after acknowledgement")
	}
	s.Reopen(ui.Services{}, d)
	if reopened != 1 {
		t.Fatal("explicit original reopen was not delivered")
	}
	active.Generation++
	s.Reopen(ui.Services{}, d)
	if reopened != 1 || s.CanReopen(d) {
		t.Fatal("reopen reached replacement generation")
	}
}

func TestPageFailureRetiresOnlyFeatureBoundOriginalDocument(t *testing.T) {
	for _, read := range []string{"refresh", "next"} {
		for _, fail := range []struct {
			name string
			err  error
		}{{"lost", errors.New("original read lost")}, {"canceled", context.Canceled}, {"missing", nil}} {
			t.Run(read+"/"+fail.name, func(t *testing.T) {
				active := Session{ProfileID: 12, Generation: 4, Connected: true}
				var calls atomic.Int32
				_, cancel := context.WithCancel(context.Background())
				lease := &testDocumentLease{cancel: cancel, runtime: Runtime{PageFailureRequiresReopen: true, AdvancedLoader: func(context.Context, int64, string, string, contract.DataOptions) (*contract.DataPage, error) {
					calls.Add(1)
					return nil, fail.err
				}}}
				s := New(Dependencies{Session: func() Session { return active }, Runtime: func() Runtime {
					return Runtime{ReserveDocument: func(contract.ObjectRef) (contract.DataDocumentLease, error) { return lease, nil }}
				}})
				d := s.OpenObject(1, contract.NamespaceRef{ProfileID: 12, Catalog: "original"}.Object("relationship", "same"), contract.Capabilities{})
				defer s.Close(d)
				cached := &contract.DataPage{Offset: 200, KeysetAvailable: true, Columns: []string{"original"}, CurrentCursor: &contract.DataCursor{Offset: 200}, NextCursor: &contract.DataCursor{Offset: 300}}
				d.page, d.offset = cached, 200
				if read == "next" {
					s.changeMySQLDataPage(ui.Services{}, d, 300)
				} else {
					s.Load(ui.Services{}, d, true)
				}
				if !d.loading || d.page != cached {
					t.Fatal("pending page discarded successful cache")
				}
				parkRetirementPage(t, d)
				s.Consume(ui.Services{})
				s.Load(ui.Services{}, d, true)
				if d.Kind() != "关系类型" || !d.readRetired || d.loading || d.page != cached || d.offset != 200 || d.after == nil || d.after.Offset != 200 || calls.Load() != 1 || d.err == "" {
					t.Fatal("failed bound page replayed or replaced cache")
				}
			})
		}
	}
}

func awaitRetirementSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("original executor stalled")
	}
}
func parkRetirementPage(t *testing.T, d *Document) {
	t.Helper()
	select {
	case result := <-d.results:
		out := make(chan mysqlDataResult, 1)
		out <- result
		d.results = out
	case <-time.After(2 * time.Second):
		t.Fatal("original page stalled")
	}
}
func parkRetirementAuxiliary(t *testing.T, p *Panel) {
	t.Helper()
	select {
	case result := <-p.info.countResults:
		out := make(chan countResult, 1)
		out <- result
		p.info.countResults = out
	case <-time.After(2 * time.Second):
		t.Fatal("original count stalled")
	}
	select {
	case result := <-p.chunkResults:
		out := make(chan chunkResult, 1)
		out <- result
		p.chunkResults = out
	case <-time.After(2 * time.Second):
		t.Fatal("original field stalled")
	}
}

func TestDocumentKeysetRejectsUnseenJumpBeforeReadAndPreservesCachedRange(t *testing.T) {
	active := Session{ProfileID: 12, Generation: 4, Connected: true}
	var calls atomic.Int32
	_, cancel := context.WithCancel(context.Background())
	lease := &testDocumentLease{cancel: cancel, runtime: Runtime{PageFailureRequiresReopen: true, Capabilities: contract.Capabilities{Keyset: true}, AdvancedLoader: func(_ context.Context, _ int64, _, _ string, o contract.DataOptions) (*contract.DataPage, error) {
		calls.Add(1)
		if o.After == nil || o.After.Offset != 100 {
			return nil, contract.ErrDataPage
		}
		return &contract.DataPage{Offset: 100, KeysetAvailable: true, Columns: []string{"element_id"}}, nil
	}}}
	s := New(Dependencies{Session: func() Session { return active }, Runtime: func() Runtime {
		return Runtime{ReserveDocument: func(contract.ObjectRef) (contract.DataDocumentLease, error) { return lease, nil }}
	}})
	d := s.OpenObject(1, contract.NamespaceRef{ProfileID: 12, Catalog: "original"}.Object("label", "same"), lease.runtime.Capabilities)
	defer s.Close(d)
	cached := &contract.DataPage{KeysetAvailable: true, HasMore: true, Columns: []string{"element_id"}, NextCursor: &contract.DataCursor{Offset: 100, Values: [][]byte{[]byte("original-boundary")}}}
	d.page = cached
	d.pageNumberDraft = "3"
	if s.jumpDataPage(ui.Services{}, d) || d.pageNumberError == "" || calls.Load() != 0 || d.page != cached || d.loading || d.readRetired {
		t.Fatal("unseen page jump dispatched or retired cached native range")
	}
	s.changeMySQLDataPage(ui.Services{}, d, 300)
	if calls.Load() != 0 || d.page != cached || d.loading {
		t.Fatal("direct unseen page bypassed boundary guard")
	}
	d.pageNumberDraft = "2"
	if !s.jumpDataPage(ui.Services{}, d) {
		t.Fatal("known next boundary rejected", d.pageNumberError)
	}
	parkRetirementPage(t, d)
	s.Consume(ui.Services{})
	if d.loading || d.readRetired || d.err != "" || d.offset != 100 || calls.Load() != 1 {
		t.Fatal("known next cursor did not preserve original read")
	}
}
