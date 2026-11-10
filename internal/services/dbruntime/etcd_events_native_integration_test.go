package dbruntime_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
)

func TestEtcdNativeEventsRuntimeOriginalPublicationAndExplicitRecovery(t *testing.T) {
	if os.Getenv("NYADB_ETCD_EVENTS_IT") != "1" {
		t.Skip("requires owned original events runtime fixture")
	}
	p, secret := etcdRuntimeProfile(t)
	m := etcdRuntimeManager(t)
	published, err := m.ConnectSession(context.Background(), p, secret)
	if err != nil {
		t.Fatal(err)
	}
	writer := etcdRuntimeReserve(t, m, 1)
	key := append([]byte("/events-runtime/"+t.Name()), 0, 255)
	first := etcdRuntimeDo(t, writer, "KV/Put", &pb.PutRequest{Key: key, Value: []byte{0, 255}})
	provider, err := m.KVEvents(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	watchLease, err := provider.ReserveKVEvents(2)
	if err != nil {
		t.Fatal(err)
	}
	defer watchLease.Close()
	if watchLease.Scope().Generation != published.Generation() || watchLease.Scope().Header.MemberID != first.Header.MemberID {
		t.Fatal("events publication lost original contextual generation/member")
	}
	w, err := watchLease.StartWatch(context.Background(), contract.KVWatchRequest{Key: bytes.Clone(key), WatchID: 91, StartRevision: first.Header.Revision, PrevKV: true})
	if err != nil {
		t.Fatal(err)
	}
	var received contract.KVWatchBatch
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		b := w.Poll()
		received.Frames = append(received.Frames, b.Frames...)
		found := false
		for _, f := range received.Frames {
			for _, e := range f.Events {
				found = found || bytes.Equal(e.Entry.Key, key) && bytes.Equal(e.Entry.Value, []byte{0, 255})
			}
		}
		if found {
			break
		}
		if b.Ended {
			t.Fatal("original published Watch ended before its receipt")
		}
		time.Sleep(10 * time.Millisecond)
	}
	count := 0
	for _, f := range received.Frames {
		count += len(f.Events)
	}
	if count != 1 {
		t.Fatal("published original event not received")
	}
	last := etcdRuntimeDo(t, writer, "KV/Put", &pb.PutRequest{Key: key, Value: []byte{1, 255}})
	historyLease, err := provider.ReserveKVEvents(3)
	if err != nil {
		t.Fatal(err)
	}
	defer historyLease.Close()
	history, err := historyLease.History(context.Background(), contract.KVHistoryRequest{Key: key, StartRevision: first.Header.Revision, EndRevision: last.Header.Revision, Limit: 100})
	if err != nil || !history.Complete || len(history.Events) != 2 {
		t.Fatal("published original history failed", err)
	}
	etcdRuntimeDo(t, writer, "KV/Put", &pb.PutRequest{Key: key, Value: []byte("after-original-window")})
	replacement, err := m.ConnectSession(context.Background(), p, secret)
	if err != nil {
		t.Fatal(err)
	}
	if replacement.Generation() <= published.Generation() || !errors.Is(watchLease.CheckKVLease(), contract.ErrRevoked) || !errors.Is(historyLease.CheckKVLease(), contract.ErrRevoked) {
		t.Fatal("publication replacement reused old event leases")
	}
	terminal := w.Poll()
	if !terminal.Ended || !terminal.Incomplete {
		t.Fatal("original published receiver did not settle")
	}
	fresh, err := m.KVEvents(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	recovery, err := fresh.RecoverKVEvents(historyLease)
	if err != nil || recovery.Lease == nil || recovery.Lease.Scope().Generation != replacement.Generation() || recovery.History == nil || recovery.History.Window.EndRevision != last.Header.Revision || !bytes.Equal(recovery.History.Initial.Native, history.Initial.Native) || len(recovery.History.Events) != 2 {
		t.Fatal("recovery substituted original history receipt", err)
	}
	defer recovery.Lease.Close()
	if recovery.Lease.LastHistory() != nil {
		t.Fatal("recovery executed history")
	}
	watchRecovery, err := fresh.RecoverKVEvents(watchLease)
	if err != nil || watchRecovery.Lease == nil || watchRecovery.Lease.Scope().Generation != replacement.Generation() {
		t.Fatal("original Watch recovery failed", err)
	}
	defer watchRecovery.Lease.Close()
	if err = watchRecovery.Lease.Preconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	currentWriter := etcdRuntimeReserve(t, m, 4)
	current := etcdRuntimeDo(t, currentWriter, "KV/Range", &pb.RangeRequest{Key: key})
	if len(current.Reply.Entries) != 1 || !bytes.Equal(current.Reply.Entries[0].Value, []byte("after-original-window")) || current.Reply.Entries[0].Version != 3 {
		t.Fatal("event/history recovery replayed original requests")
	}
}
