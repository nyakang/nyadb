package dbruntime_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
)

type prefixRuntimeSet struct {
	snapshot contract.DataKeyPrefixDeleteSetSnapshot
	closed   atomic.Int32
	preview  func(context.Context, int) (contract.DataMutationPlan, error)
}

func (s *prefixRuntimeSet) Snapshot() contract.DataKeyPrefixDeleteSetSnapshot {
	return contract.CloneDataKeyPrefixDeleteSetSnapshot(s.snapshot)
}
func (s *prefixRuntimeSet) Close() error { s.closed.Add(1); return nil }
func (s *prefixRuntimeSet) PreviewBatch(ctx context.Context, index int) (contract.DataMutationPlan, error) {
	return s.preview(ctx, index)
}

type prefixRuntimeSetHandle struct {
	mutationHandle
	calls   atomic.Int32
	set     *prefixRuntimeSet
	produce func(contract.DataKeyPrefixDelete) (contract.DataKeyPrefixDeleteSet, error)
}

func (h *prefixRuntimeSetHandle) DataRuntime() contract.DataRuntime {
	r := h.mutationHandle.DataRuntime()
	r.CaptureKeyPrefixDelete = func(_ context.Context, _ int64, _, _ string, request contract.DataKeyPrefixDelete) (contract.DataKeyPrefixDeleteSet, error) {
		h.calls.Add(1)
		if h.produce != nil {
			return h.produce(request)
		}
		h.set.snapshot.Request = request
		return h.set, nil
	}
	return r
}
func prefixSetRuntime(t *testing.T) (*dbruntime.Manager, *[]*prefixRuntimeSetHandle) {
	t.Helper()
	m := dbruntime.New()
	t.Cleanup(func() { m.Close() })
	handles := []*prefixRuntimeSetHandle{}
	m.Register(connections.Consul, dbruntime.ProviderFunc(func(_ context.Context, p connections.Profile, _ string) (contract.Session, error) {
		h := &prefixRuntimeSetHandle{mutationHandle: mutationHandle{handle: handle{id: p.ID, caps: contract.Capabilities{EditableTables: true, KeyValues: true}}}}
		o := h.Namespace("main").Object("table", "owned")
		h.set = &prefixRuntimeSet{snapshot: contract.DataKeyPrefixDeleteSetSnapshot{BatchSize: 21, NativeJSON: `{"original-complete-capture":true}`}}
		for _, key := range []string{"scope/a", "scope/b"} {
			h.set.snapshot.Candidates = append(h.set.snapshot.Candidates, &contract.RowSnapshot{Mode: "fingerprints", Ref: contract.RowRef{Object: o, Columns: []string{"opaque"}, Values: [][]byte{[]byte("signed-original:" + key)}, TypedValues: []contract.Cell{{Kind: contract.Text, Text: key}}}})
		}
		h.set.preview = func(_ context.Context, index int) (contract.DataMutationPlan, error) {
			request := h.set.snapshot.Request
			batch := contract.DataKeyPrefixDeleteBatch{Request: request, Index: index, Offset: 0, Count: 2, Total: 2}
			h.plan = &mutationPreview{snapshot: contract.DataMutationSnapshot{Object: request.Object, KeyPrefixDelete: &request, KeyPrefixDeleteBatch: &batch, NativeJSON: `{"original-batch":true}`}}
			for _, original := range h.set.snapshot.Candidates {
				h.plan.snapshot.Edits = append(h.plan.snapshot.Edits, contract.DataEdit{Kind: "delete", Original: original})
			}
			return h.plan, nil
		}
		handles = append(handles, h)
		return h, nil
	}))
	if _, err := m.ConnectSession(context.Background(), keyCopyProfile(), ""); err != nil {
		t.Fatal(err)
	}
	return m, &handles
}

func TestDataKeyPrefixDeleteSetRuntimeOriginalCaptureBatchAndImmutableScope(t *testing.T) {
	m, handles := prefixSetRuntime(t)
	h := (*handles)[0]
	r := m.Data(1)
	request := contract.DataKeyPrefixDelete{Object: h.set.snapshot.Candidates[0].Ref.Object, Prefix: "scope/"}
	foreign := request
	foreign.Object.ProfileID = 2
	if set, err := r.CaptureKeyPrefixDelete(context.Background(), 1, "main", "owned", foreign); set != nil || !errors.Is(err, connections.ErrInvalid) || h.calls.Load() != 0 {
		t.Fatal("foreign capture reached original provider", err)
	}
	set, err := r.CaptureKeyPrefixDelete(context.Background(), 1, "main", "owned", request)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := set.Snapshot()
	snapshot.Candidates[0].Ref.Values[0][0] = '!'
	if set.Snapshot().Candidates[0].Ref.Values[0][0] == '!' {
		t.Fatal("set inspection changed original authority")
	}
	if plan, err := set.PreviewBatch(context.Background(), 1); plan != nil || !errors.Is(err, connections.ErrInvalid) {
		t.Fatal("foreign batch admitted", err)
	}
	plan, err := set.PreviewBatch(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.SubmitEdits(context.Background(), 1, "main", "owned", h.plan); !errors.Is(err, connections.ErrInvalid) {
		t.Fatal("raw batch bypassed runtime binding", err)
	}
	if _, err = r.SubmitEdits(context.Background(), 1, "main", "foreign", plan); !errors.Is(err, connections.ErrInvalid) {
		t.Fatal("batch crossed original object", err)
	}
	if _, err = r.SubmitEdits(context.Background(), 1, "main", "owned", plan); err != nil || h.saved.Load() != 1 {
		t.Fatal("original batch not forwarded", err)
	}
	set.Close()
	if h.set.closed.Load() != 1 {
		t.Fatal("original capture not closed")
	}
}

func TestDataKeyPrefixDeleteSetRuntimeRejectsForeignAndMissingBatchDescriptors(t *testing.T) {
	for _, fault := range []string{"descriptor", "count", "foreign-original", "authored-values", "nil-plan"} {
		t.Run(fault, func(t *testing.T) {
			m, handles := prefixSetRuntime(t)
			h := (*handles)[0]
			request := contract.DataKeyPrefixDelete{Object: h.set.snapshot.Candidates[0].Ref.Object, Prefix: "scope/"}
			set, err := m.Data(1).CaptureKeyPrefixDelete(context.Background(), 1, "main", "owned", request)
			if err != nil {
				t.Fatal(err)
			}
			original := h.set.preview
			h.set.preview = func(ctx context.Context, index int) (contract.DataMutationPlan, error) {
				plan, err := original(ctx, index)
				switch fault {
				case "descriptor":
					h.plan.snapshot.KeyPrefixDeleteBatch = nil
				case "count":
					h.plan.snapshot.KeyPrefixDeleteBatch.Total = 1
				case "foreign-original":
					h.plan.snapshot.Edits[0].Original = &contract.RowSnapshot{}
				case "authored-values":
					h.plan.snapshot.Edits[0].Values = map[string]contract.DataValue{"key": {Kind: contract.Text, Text: "foreign"}}
				case "nil-plan":
					return nil, nil
				}
				return plan, err
			}
			if plan, err := set.PreviewBatch(context.Background(), 0); plan != nil || err == nil {
				t.Fatal("invalid batch acquired original authority")
			}
			if fault != "nil-plan" && h.plan.closed.Load() != 1 {
				t.Fatal("invalid original batch retained handle")
			}
		})
	}
}

func TestDataKeyPrefixDeleteSetRuntimeLateCaptureClosesOnlyOriginalHandle(t *testing.T) {
	m, handles := prefixSetRuntime(t)
	h := (*handles)[0]
	r := m.Data(1)
	request := contract.DataKeyPrefixDelete{Object: h.set.snapshot.Candidates[0].Ref.Object, Prefix: "scope/"}
	entered, release := make(chan struct{}), make(chan struct{})
	h.produce = func(request contract.DataKeyPrefixDelete) (contract.DataKeyPrefixDeleteSet, error) {
		h.set.snapshot.Request = request
		close(entered)
		<-release
		return h.set, nil
	}
	type completion struct {
		set contract.DataKeyPrefixDeleteSet
		err error
	}
	done := make(chan completion, 1)
	go func() {
		set, err := r.CaptureKeyPrefixDelete(context.Background(), 1, "main", "owned", request)
		done <- completion{set, err}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("capture did not enter")
	}
	m.Disconnect(1)
	if _, err := m.ConnectSession(context.Background(), keyCopyProfile(), ""); err != nil {
		t.Fatal(err)
	}
	close(release)
	select {
	case c := <-done:
		if c.set != nil || !errors.Is(c.err, contract.ErrRevoked) {
			t.Fatal("late capture followed replacement", c.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("late capture stalled")
	}
	if h.set.closed.Load() != 1 || (*handles)[1].set.closed.Load() != 0 || (*handles)[1].calls.Load() != 0 {
		t.Fatal("late capture touched replacement or retained original")
	}
}

func TestDataKeyPrefixDeleteSetRuntimeRejectsStoppedBusyAndUncertainBeforeProvider(t *testing.T) {
	for _, state := range []string{"stopped", "busy", "uncertain", "advanced"} {
		t.Run(state, func(t *testing.T) {
			m, handles := prefixSetRuntime(t)
			h := (*handles)[0]
			request := contract.DataKeyPrefixDelete{Object: h.set.snapshot.Candidates[0].Ref.Object, Prefix: "scope/"}
			set, err := m.Data(1).CaptureKeyPrefixDelete(context.Background(), 1, "main", "owned", request)
			if err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			original := h.set.preview
			h.set.preview = func(ctx context.Context, index int) (contract.DataMutationPlan, error) {
				calls.Add(1)
				return original(ctx, index)
			}
			var expected error
			switch state {
			case "stopped":
				h.set.snapshot.Stopped, expected = true, contract.ErrDataConflict
			case "busy":
				h.set.snapshot.InFlight, expected = true, contract.ErrKVBusy
			case "uncertain":
				h.set.snapshot.Uncertain, expected = true, contract.ErrDataCommitUncertain
			case "advanced":
				h.set.snapshot.NextBatch, expected = 1, connections.ErrInvalid
			}
			if plan, err := set.PreviewBatch(context.Background(), 0); plan != nil || !errors.Is(err, expected) || calls.Load() != 0 {
				t.Fatal("inadmissible batch reached the original provider", err)
			}
		})
	}
}
