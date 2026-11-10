package etcd

import (
	"bytes"
	"context"
	"errors"
	"testing"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/api/v3/mvccpb"
)

func TestHistoryDefaultWindowUsesOriginalKeyRevisionWithoutChangingExplicitStart(t *testing.T) {
	for _, test := range []struct{ requested, target, want int64 }{{0, 1, 1}, {0, 9999, 1}, {0, 10000, 1}, {0, 10001, 2}, {0, 24000, 14001}, {7, 24000, 7}} {
		if got := historyStart(test.requested, test.target); got != test.want {
			t.Fatalf("history start=%d want=%d", got, test.want)
		}
	}
}

func TestHistorySnapshotsPreserveOriginalReceiptAndIndependentProjectedBytes(t *testing.T) {
	event := &mvccpb.Event{Kv: &mvccpb.KeyValue{Key: []byte{0, 255}, Value: []byte{1}}, PrevKv: &mvccpb.KeyValue{Key: []byte{0, 255}, Value: []byte{2}}}
	r := HistoryResult{Authored: HistoryRequest{Key: []byte{0, 255}}, Window: HistoryRequest{Key: []byte{0, 255}},
		Initial: Response{native: &pb.RangeResponse{Kvs: []*mvccpb.KeyValue{{Key: []byte{0, 255}}}}},
		Frames:  []WatchFrame{{Native: []byte{3}, Events: []*mvccpb.Event{event}}}, Events: []*mvccpb.Event{event}}
	snapshot := r.Clone()
	snapshot.Authored.Key[0], snapshot.Window.Key[0], snapshot.Frames[0].Native[0] = 9, 9, 9
	snapshot.Frames[0].Events[0].Kv.Key[0] = 9
	snapshot.Events[0].PrevKv.Value[0] = 9
	snapshot.Initial.native.(*pb.RangeResponse).Kvs[0].Key[0] = 9
	if r.Authored.Key[0] != 0 || r.Window.Key[0] != 0 || r.Frames[0].Native[0] != 3 || event.Kv.Key[0] != 0 || event.PrevKv.Value[0] != 2 || r.Initial.native.(*pb.RangeResponse).Kvs[0].Key[0] != 0 || snapshot.Frames[0].Events[0].PrevKv.Value[0] != 2 {
		t.Fatal("history snapshots alias original or another projection")
	}
}

func TestHistoryRejectsConcurrentOrCanceledCaptureBeforeNativeIO(t *testing.T) {
	c := sessionIdentityClient(2)
	defer c.Close()
	r := HistoryRequest{Key: []byte{0, 255}, Limit: 1}
	c.operation.Lock()
	result, err := c.History(context.Background(), r)
	c.operation.Unlock()
	if !errors.Is(err, ErrBusy) || result.Initial.Attempted || result.Complete || !c.Alive() {
		t.Fatal("concurrent history altered original executor", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err = c.History(ctx, r)
	if !errors.Is(err, context.Canceled) || result.Initial.Attempted || result.Complete || !c.Alive() {
		t.Fatal("canceled history attempted native capture", err)
	}
}
func TestHistoryRequestBoundsAndByteSnapshotsRemainExplicit(t *testing.T) {
	original := HistoryRequest{Key: []byte{0, 255}, StartRevision: 2, EndRevision: 3, Limit: 500}
	snapshot := original.clone()
	original.Key[0] = 4
	if !bytes.Equal(snapshot.Key, []byte{0, 255}) || validateHistoryRequest(snapshot) != nil {
		t.Fatal("history window lost original complete byte key")
	}
	for _, bad := range []HistoryRequest{{Key: []byte("a"), StartRevision: 3, EndRevision: 2, Limit: 1}, {Key: []byte("a"), StartRevision: -1, Limit: 1}, {Key: []byte("a"), EndRevision: -1, Limit: 1}, {Key: []byte("a"), Limit: 501}, {Key: []byte("a"), Limit: 0}, {Key: bytes.Repeat([]byte{255}, 4097), Limit: 1}} {
		if validateHistoryRequest(bad) == nil {
			t.Fatal("unsupported history request silently changed", bad.StartRevision, bad.EndRevision, bad.Limit)
		}
	}
}
