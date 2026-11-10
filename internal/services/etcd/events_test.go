package etcd

import (
	"bytes"
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/api/v3/mvccpb"
	"google.golang.org/protobuf/proto"
)

func eventsTestSession(t *testing.T) *Session {
	t.Helper()
	p, _ := connections.Defaults(connections.Etcd)
	p.ID = 1
	s := newSession(p, "", sessionIdentityClient(2))
	t.Cleanup(func() {
		_ = s.Close()
		for l := range s.events {
			_ = l.Close()
		}
	})
	return s
}

func eventsTestLease(t *testing.T, s *Session, document uint64) *eventsLease {
	t.Helper()
	l, err := s.ReserveKVEvents(document)
	if err != nil {
		t.Fatal(err)
	}
	return l.(*eventsLease)
}

func TestEventsReservationBoundsMixedDocumentsAndDoesNotOpenChannels(t *testing.T) {
	s := eventsTestSession(t)
	var opens atomic.Int32
	s.opener = func(context.Context, connections.Profile, string) (*Client, error) {
		opens.Add(1)
		return nil, ErrInvalid
	}
	for i := uint64(1); i <= 4; i++ {
		eventsTestLease(t, s, i)
	}
	if _, err := s.ReserveKVEvents(5); !errors.Is(err, contract.ErrKVLeaseLimit) {
		t.Fatal("fifth event document admitted", err)
	}
	if _, err := s.ReserveKVEvents(1); err == nil {
		t.Fatal("duplicate event document admitted")
	}
	for i := uint64(1); i <= 28; i++ {
		if _, err := s.ReserveKV(i); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.ReserveKV(29); !errors.Is(err, contract.ErrKVLeaseLimit) {
		t.Fatal("events bypassed total document limit", err)
	}
	if _, err := s.ReserveDataDocument(s.KVScope().Namespace.Object("keyspace", "keys")); !errors.Is(err, contract.ErrKVLeaseLimit) {
		t.Fatal("data document bypassed event reservation", err)
	}
	if opens.Load() != 0 {
		t.Fatal("reservation performed connection I/O")
	}
}

func TestEventsSharedBudgetCountsTransferredReceiptsUntilExplicitDisposal(t *testing.T) {
	s := eventsTestSession(t)
	a, b, c := eventsTestLease(t, s, 1), eventsTestLease(t, s, 2), eventsTestLease(t, s, 3)
	if !a.admit(3<<20) || !b.admit(3<<20) || c.admit(3<<20) || a.admit(2<<20) || s.eventBytes != 24<<20 {
		t.Fatal("document/session byte budgets were not enforced")
	}
	// Poll transfers a frame into its original document; it does not erase its
	// already reserved owned-byte charge or make another document fit.
	w := watchObservationFixture()
	w.reserve = c.admit
	if err := w.append(WatchFrame{Native: []byte{0, 255}}); err != nil {
		t.Fatal(err)
	}
	charged := s.eventBytes
	if len(w.Poll()) != 1 || s.eventBytes != charged {
		t.Fatal("Poll released retained receipt reservation")
	}
	if err := a.ClearReceipts(); err != nil {
		t.Fatal(err)
	}
	if !c.admit(3<<20) || a.charged != 0 {
		t.Fatal("explicit disposal failed to release the original budget")
	}
	if err := b.Close(); err != nil || b.charged != 0 {
		t.Fatal("document close retained reservation", err)
	}
}

func TestEventsRecoveryRejectsForeignScopeBeforeChangingTheOriginal(t *testing.T) {
	first := eventsTestSession(t)
	old := eventsTestLease(t, first, 1)
	p := first.profile
	p.EtcdOptions.Prefix = "different"
	second := newSession(p, "", sessionIdentityClient(2))
	defer second.Close()
	if _, err := second.RecoverKVEvents(old); !errors.Is(err, contract.ErrRevoked) || old.CheckKVLease() != nil {
		t.Fatal("foreign recovery altered original lease", err)
	}
	var typedNil *eventsLease
	if _, err := second.RecoverKVEvents(typedNil); !errors.Is(err, contract.ErrKVPlanUsed) {
		t.Fatal("typed nil event recovery admitted", err)
	}
	var opens atomic.Int32
	first.opener = func(context.Context, connections.Profile, string) (*Client, error) {
		opens.Add(1)
		return nil, ErrInvalid
	}
	fresh, err := first.RecoverKVEvents(old)
	if err != nil || fresh.Lease == nil || opens.Load() != 0 || !errors.Is(old.CheckKVLease(), contract.ErrRevoked) {
		t.Fatal("recovery replayed/opened or failed to settle original", err)
	}
}

func TestEventsSessionRetirementKeepsParkedOriginalFramesAndHistoryUntilDocumentClose(t *testing.T) {
	s := eventsTestSession(t)
	l := eventsTestLease(t, s, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	close(done)
	w := &WatchRun{ctx: ctx, cancel: cancel, done: done, ended: true, terminal: WatchTerminal{Reason: "interrupted"}}
	original := &pb.WatchResponse{Header: watchTestHeader(7), WatchId: 1, Created: true}
	raw, err := proto.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	if err = w.append(WatchFrame{Native: raw, Header: header(original.Header), WatchID: 1, Created: true}); err != nil {
		t.Fatal(err)
	}
	l.run = &eventsRun{lease: l, raw: w, request: contract.KVWatchRequest{Key: []byte{0, 255}, WatchID: 1}}
	l.last = &contract.KVHistoryResult{Initial: contract.KVResult{Native: []byte{7}}, Events: []contract.KVEvent{{Entry: contract.KVEntry{Key: []byte{0, 255}}}}}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	batch := l.run.Poll()
	if !batch.Ended || len(batch.Frames) != 1 || !bytes.Equal(batch.Frames[0].Native, raw) || l.LastHistory() == nil {
		t.Fatal("session retirement discarded parked original receipt")
	}
	if err = l.Close(); err != nil || l.LastHistory() != nil || l.run != nil {
		t.Fatal("explicit document close retained caches", err)
	}
}

func TestEventProjectionRetainsUnknownOriginalFieldsAndIndependentBinaryValues(t *testing.T) {
	r := &pb.WatchResponse{Header: watchTestHeader(7), WatchId: 1, Events: []*mvccpb.Event{{Kv: &mvccpb.KeyValue{Key: []byte{0, 255}, Value: []byte{1, 255}, Lease: 9223372036854775807}, PrevKv: &mvccpb.KeyValue{Key: []byte{0, 255}, Value: []byte{2}}}}}
	r.ProtoReflect().SetUnknown([]byte{0xa0, 0x06, 0x07})
	raw, err := proto.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	f := projectEventFrame(WatchFrame{Native: raw, Header: header(r.Header), WatchID: 1, Events: r.Events})
	r.Events[0].Kv.Value[0] = 9
	raw[0] ^= 255
	original := &pb.WatchResponse{}
	if err = proto.Unmarshal(f.Native, original); err != nil || !bytes.Equal(original.ProtoReflect().GetUnknown(), []byte{0xa0, 0x06, 0x07}) || f.Events[0].Entry.Value[0] != 1 || f.Events[0].Entry.Lease != 9223372036854775807 || f.Events[0].Previous.Value[0] != 2 {
		t.Fatal("projection replaced or aliased original native frame", err)
	}
}
