package etcd

import (
	"bytes"
	"errors"
	"testing"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/api/v3/mvccpb"
	"google.golang.org/protobuf/proto"
)

func watchObservationFixture() *WatchRun {
	return &WatchRun{client: &Client{identity: Identity{Header: Header{ClusterID: 1, MemberID: 2}}}, request: &pb.WatchCreateRequest{Key: []byte("a"), WatchId: 42, StartRevision: 2, PrevKv: true}, minimumRevision: 2}
}
func watchTestHeader(revision int64) *pb.ResponseHeader {
	return &pb.ResponseHeader{ClusterId: 1, MemberId: 2, Revision: revision, RaftTerm: 1}
}
func startWatchObservation(t *testing.T, w *WatchRun) {
	t.Helper()
	if _, err := w.observe(&pb.WatchResponse{WatchId: 42, Created: true, Header: watchTestHeader(7)}); err != nil {
		t.Fatal(err)
	}
}
func TestWatchRetainsOriginalUnknownFieldsAndIndependentEventBytes(t *testing.T) {
	w := watchObservationFixture()
	startWatchObservation(t, w)
	r := &pb.WatchResponse{WatchId: 42, Header: watchTestHeader(8), Events: []*mvccpb.Event{{Type: mvccpb.PUT, Kv: &mvccpb.KeyValue{Key: []byte("a"), Value: []byte{0, 255}, CreateRevision: 2, ModRevision: 3, Version: 2}, PrevKv: &mvccpb.KeyValue{Key: []byte("a"), Value: []byte("before"), CreateRevision: 2, ModRevision: 2, Version: 1}}}}
	r.ProtoReflect().SetUnknown([]byte{0xa0, 0x06, 0x07})
	frame, err := w.observe(r)
	if err != nil {
		t.Fatal(err)
	}
	r.Events[0].Kv.Value[0] = 99
	original := &pb.WatchResponse{}
	if err := proto.Unmarshal(frame.Native, original); err != nil || !bytes.Equal(original.ProtoReflect().GetUnknown(), []byte{0xa0, 0x06, 0x07}) || frame.Events[0].Kv.Value[0] != 0 {
		t.Fatal("original receipt/projection changed", err)
	}
	if err = w.append(frame); err != nil {
		t.Fatal(err)
	}
	pages := w.Poll()
	if len(pages) != 1 || w.bufferedBytes != 0 || w.bufferedEvents != 0 || len(w.frames) != 0 {
		t.Fatal("poll failed to transfer original owned buffer")
	}
}
func TestWatchAcceptsOnlyOriginalProgressAndCompactionTerminalReceipts(t *testing.T) {
	w := watchObservationFixture()
	startWatchObservation(t, w)
	progress := &pb.WatchResponse{WatchId: -1, Header: watchTestHeader(8)}
	if _, err := w.observe(progress); !errors.Is(err, ErrProtocol) {
		t.Fatal("unsolicited foreign progress admitted")
	}
	w.pendingProgress = 1
	if f, err := w.observe(progress); err != nil || !f.Progress || w.pendingProgress != 0 {
		t.Fatal("own requested broadcast progress rejected", err)
	}
	compact := &pb.WatchResponse{WatchId: 42, Canceled: true, CompactRevision: 5, Header: watchTestHeader(0)}
	f, err := w.observe(compact)
	if err != nil || f.Header.Revision != 0 || f.Header.ClusterID != 1 || f.Header.MemberID != 2 || f.CompactRevision != 5 {
		t.Fatal("original zero revision compaction was replaced/rejected", err)
	}
	compact.WatchId = 43
	if _, err = w.observe(compact); !errors.Is(err, ErrProtocol) {
		t.Fatal("foreign compaction admitted")
	}
	denied := watchObservationFixture()
	f, err = denied.observe(&pb.WatchResponse{WatchId: -1, Created: true, Canceled: true, CancelReason: "denied", Header: watchTestHeader(7)})
	if err != nil || denied.started || !f.Canceled {
		t.Fatal("failed original creation admitted as a running watch", err)
	}
}
func TestWatchRejectsForeignEventsAndKeepsSameRevisionOriginalOrder(t *testing.T) {
	for _, bad := range []string{"identity", "key", "revision", "delete-value", "unexpected-prev", "maximum-key"} {
		t.Run(bad, func(t *testing.T) {
			w := watchObservationFixture()
			startWatchObservation(t, w)
			r := &pb.WatchResponse{WatchId: 42, Header: watchTestHeader(8), Events: []*mvccpb.Event{{Type: mvccpb.DELETE, Kv: &mvccpb.KeyValue{Key: []byte("a"), ModRevision: 6}}}}
			switch bad {
			case "identity":
				r.Header.MemberId = 3
			case "key":
				r.Events[0].Kv.Key = []byte("b")
			case "revision":
				r.Events[0].Kv.ModRevision = 9
			case "delete-value":
				r.Events[0].Kv.Value = []byte("not a delete")
			case "unexpected-prev":
				r.Events[0].PrevKv = &mvccpb.KeyValue{Key: []byte("b"), CreateRevision: 2, ModRevision: 3, Version: 1}
			case "maximum-key":
				r.Events[0].Kv.Key = bytes.Repeat([]byte{255}, 4097)
			}
			if _, err := w.observe(r); err == nil {
				t.Fatal("invalid original event accepted")
			}
		})
	}
	w := watchObservationFixture()
	w.request.RangeEnd = []byte("c")
	startWatchObservation(t, w)
	r := &pb.WatchResponse{WatchId: 42, Header: watchTestHeader(8), Events: []*mvccpb.Event{{Type: mvccpb.DELETE, Kv: &mvccpb.KeyValue{Key: []byte("a"), ModRevision: 6}}, {Type: mvccpb.DELETE, Kv: &mvccpb.KeyValue{Key: []byte("b"), ModRevision: 6}}}}
	f, err := w.observe(r)
	if err != nil || len(f.Events) != 2 || !bytes.Equal(f.Events[1].Kv.Key, []byte("b")) {
		t.Fatal("same revision events were reordered or omitted", err)
	}
}
func TestWatchBudgetDoesNotEvictPreviouslyAcceptedCompleteReceipts(t *testing.T) {
	w := watchObservationFixture()
	first := WatchFrame{Native: bytes.Repeat([]byte{1}, watchByteLimit/8)}
	if err := w.append(first); err != nil {
		t.Fatal(err)
	}
	before := w.bufferedBytes
	if err := w.append(WatchFrame{Native: bytes.Repeat([]byte{2}, watchByteLimit/4)}); !errors.Is(err, ErrBounds) || w.bufferedBytes != before || len(w.frames) != 1 {
		t.Fatal("overflow evicted or partially captured original frames")
	}
	if frames := w.Poll(); len(frames) != 1 || !bytes.Equal(frames[0].Native, first.Native) {
		t.Fatal("overflow changed original receipt")
	}
}
