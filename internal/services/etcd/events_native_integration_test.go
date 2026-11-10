package etcd

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

func eventsNativeSession(t *testing.T) *Session {
	t.Helper()
	if os.Getenv("NYADB_ETCD_EVENTS_IT") != "1" {
		t.Skip("requires owned original events session fixture")
	}
	s := nativeSession(t)
	t.Cleanup(func() {
		for l := range s.events {
			_ = l.Close()
		}
	})
	return s
}

func eventsNativeWait(t *testing.T, w contract.KVWatchRun, predicate func(contract.KVWatchBatch) bool) contract.KVWatchBatch {
	t.Helper()
	var out contract.KVWatchBatch
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		b := w.Poll()
		out.Frames = append(out.Frames, b.Frames...)
		out.Ended, out.Incomplete, out.Terminal = b.Ended, b.Incomplete, b.Terminal
		if predicate(out) {
			return out
		}
		if b.Ended {
			t.Fatal("original events executor ended early", b.Terminal)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("original events receipt not observed")
	return out
}

func TestEtcdNativeEventsSessionOriginalStreamsFixedHistoryAndIndependentRequests(t *testing.T) {
	s := eventsNativeSession(t)
	admin := nativeClient(t)
	prefix := nativeKey(t)
	a := append(bytes.Clone(prefix), 0, 255)
	a = append(a, bytes.Repeat([]byte{0}, 4096-len(a))...)
	b := append(bytes.Clone(prefix), 1, 255)
	leaseID := int64(9007199254740993)
	nativeDo(t, admin, &pb.LeaseGrantRequest{ID: leaseID, TTL: 60})
	t.Cleanup(func() { nativeDo(t, admin, &pb.LeaseRevokeRequest{ID: leaseID}) })
	first := nativeDo(t, admin, &pb.PutRequest{Key: a, Value: []byte{0, 255}, Lease: leaseID})
	l := eventsTestLease(t, s, 1)
	if err := l.Preconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	scope := l.Scope()
	if scope.Generation != 1 || scope.Namespace.ProfileID != s.ProfileID() || scope.Header.MemberID != admin.Identity().MemberID {
		t.Fatal("events lease lost original scope")
	}
	rangeRequest := PrefixRange(prefix, 1, 0)
	request := contract.KVWatchRequest{Key: bytes.Clone(rangeRequest.Key), RangeEnd: bytes.Clone(rangeRequest.RangeEnd), WatchID: 71, StartRevision: first.Header.Revision, PrevKV: true}
	w, err := l.StartWatch(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	request.Key[0] ^= 255
	if !bytes.Equal(w.Request().Key, prefix) {
		t.Fatal("Watch aliases caller request")
	}
	if _, err = l.History(context.Background(), contract.KVHistoryRequest{Key: a, Limit: 100}); !errors.Is(err, contract.ErrKVBusy) {
		t.Fatal("history ran through active Watch", err)
	}
	independent := reserveNativeKV(t, s, 2)
	nativeKVDo(t, independent, &pb.RangeRequest{Key: a})
	txn := nativeDo(t, admin, &pb.TxnRequest{Success: []*pb.RequestOp{putOp(a, []byte{1, 255}, leaseID), putOp(b, []byte{2, 255}, 0)}})
	deleted := nativeDo(t, admin, &pb.DeleteRangeRequest{Key: a})
	batches := eventsNativeWait(t, w, func(b contract.KVWatchBatch) bool {
		n := 0
		for _, f := range b.Frames {
			n += len(f.Events)
		}
		return n == 4
	})
	var events []contract.KVEvent
	for _, f := range batches.Frames {
		native := &pb.WatchResponse{}
		if err = proto.Unmarshal(f.Native, native); err != nil || len(native.Events) != len(f.Events) {
			t.Fatal("native events receipt lost", err)
		}
		events = append(events, f.Events...)
	}
	if events[0].Entry.Lease != leaseID || events[1].Entry.Lease != leaseID || events[1].Entry.ModRevision != txn.Header.Revision || events[2].Entry.ModRevision != txn.Header.Revision || events[3].Type != "DELETE" || events[3].Entry.ModRevision != deleted.Header.Revision || events[3].Previous.Lease != leaseID || !bytes.Equal(events[3].Previous.Value, []byte{1, 255}) {
		t.Fatal("original same-revision order or previous bytes changed")
	}
	if err = w.Progress(); err != nil {
		t.Fatal(err)
	}
	eventsNativeWait(t, w, func(b contract.KVWatchBatch) bool {
		for _, f := range b.Frames {
			if f.Progress && f.WatchID == -1 {
				return true
			}
		}
		return false
	})
	if err = w.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	terminal := w.Poll()
	if !terminal.Ended || terminal.Incomplete || terminal.Terminal.Reason != "stopped" || len(terminal.Frames) != 1 || !terminal.Frames[0].Canceled {
		t.Fatal("original stopped receipt not retained")
	}
	if err = l.ClearReceipts(); err != nil || l.charged != 0 {
		t.Fatal("explicit original receipt disposal failed", err)
	}
	last := nativeDo(t, admin, &pb.PutRequest{Key: a, Value: []byte("recreated")})
	nativeDo(t, admin, &pb.PutRequest{Key: a, Value: []byte("future")})
	history, err := l.History(context.Background(), contract.KVHistoryRequest{Key: a, StartRevision: first.Header.Revision, EndRevision: last.Header.Revision, Limit: 500})
	if err != nil || !history.Complete || history.CaptureIncomplete || len(history.Events) != 4 || history.Events[2].Type != "DELETE" || history.Events[3].Entry.Version != 1 || history.ProgressRequests == 0 || history.Initial.Request.JSON == "" {
		t.Fatal("original history scope/receipts lost", err, history.Complete, len(history.Events))
	}
	before := l.LastHistory()
	history.Events[0].Entry.Key[0] ^= 255
	if !bytes.Equal(before.Events[0].Entry.Key, a) || !bytes.Equal(l.LastHistory().Events[0].Entry.Key, a) {
		t.Fatal("history result aliases stored original receipt")
	}
	if err = s.Close(); err != nil || l.LastHistory() == nil {
		t.Fatal("retirement lost original history evidence", err)
	}
}

func TestEtcdNativeEventsSessionOriginalRetirementRecoveryDoesNotSubscribe(t *testing.T) {
	s := eventsNativeSession(t)
	admin := nativeClient(t)
	key := nativeKey(t)
	first := nativeDo(t, admin, &pb.PutRequest{Key: key, Value: []byte{0, 255, 92}})
	l := eventsTestLease(t, s, 1)
	w, err := l.StartWatch(context.Background(), contract.KVWatchRequest{Key: key, WatchID: 92, StartRevision: first.Header.Revision, PrevKV: true})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		l.run.raw.mu.Lock()
		count := len(l.run.raw.frames)
		l.run.raw.mu.Unlock()
		if count >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	// The original receiver has stopped; its parked frames are still owned by
	// the original document and are transferred during explicit recovery.
	fresh := eventsNativeSession(t)
	opens := 0
	opener := fresh.opener
	fresh.opener = func(ctx context.Context, p connections.Profile, secret string) (*Client, error) {
		opens++
		return opener(ctx, p, secret)
	}
	recovered, err := fresh.RecoverKVEvents(l)
	if err != nil || recovered.Lease == nil || opens != 0 || !recovered.Watch.Ended || !recovered.Watch.Incomplete || len(recovered.Watch.Frames) < 2 {
		t.Fatal("recovery lost parked receipts or replayed subscription", err)
	}
	count := 0
	for _, f := range recovered.Watch.Frames {
		for _, e := range f.Events {
			if bytes.Equal(e.Entry.Key, key) && bytes.Equal(e.Entry.Value, []byte{0, 255, 92}) {
				count++
			}
		}
	}
	if count != 1 || len(w.Poll().Frames) != 0 {
		t.Fatal("original receipt not transferred exactly once")
	}
	if err = recovered.Lease.Preconnect(context.Background()); err != nil || opens != 1 {
		t.Fatal("explicit lease preconnect failed", err)
	}
	if recovered.Lease.LastHistory() != nil {
		t.Fatal("recovery executed history")
	}
	nativeDo(t, admin, &pb.PutRequest{Key: key, Value: []byte("after recovery")})
	if l.CheckKVLease() == nil {
		t.Fatal("old events lease silently recovered")
	}
	if err = recovered.Lease.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestEtcdNativeEventsHistoryDoesNotReplaceActualUnknownWriteReceipt(t *testing.T) {
	s := eventsNativeSession(t)
	admin := nativeClient(t)
	key := nativeKey(t)
	writer := reserveNativeKV(t, s, 1)
	original, fault := faultClient(t, admin, "/etcdserverpb.KV/Put", "lost")
	writer.(*kvLease).client = original
	request := nativeKVRequest(t, &pb.PutRequest{Key: key, Value: []byte{0, 255, 93}})
	plan, err := writer.Prepare(request)
	if err != nil {
		t.Fatal(err)
	}
	unknown, err := writer.Execute(context.Background(), plan)
	waitSignal(t, fault.entered)
	waitSignal(t, fault.done)
	if err == nil || !unknown.WriteOutcomeUnknown || unknown.Completed || fault.calls.Load() != 1 {
		t.Fatal("actual processed lost write did not retain uncertainty", err)
	}
	l := eventsTestLease(t, s, 2)
	history, err := l.History(context.Background(), contract.KVHistoryRequest{Key: key, Limit: 100})
	if err != nil || !history.Complete || len(history.Events) != 2 || !bytes.Equal(history.Events[1].Entry.Value, []byte("later-independent")) {
		t.Fatal("independent fixed history failed", err)
	}
	retained := writer.LastResult()
	if !retained.WriteOutcomeUnknown || retained.Completed || retained.Request != unknown.Request || retained.Code != unknown.Code {
		t.Fatal("history replaced original unknown write receipt")
	}
	if _, err = writer.Execute(context.Background(), plan); err == nil || fault.calls.Load() != 1 {
		t.Fatal("history allowed original processed write replay", err)
	}
	if !bytes.Equal(nativeDo(t, admin, &pb.RangeRequest{Key: key}).Native().(*pb.RangeResponse).Kvs[0].Value, []byte("later-independent")) {
		t.Fatal("history/recovery replayed the uncertain original write")
	}
}

func TestEtcdNativeEventsTransferredReceiptsExhaustSharedBudgetWithoutStoppingOtherRuns(t *testing.T) {
	s := eventsNativeSession(t)
	admin := nativeClient(t)
	before, after := bytes.Repeat([]byte{0}, 256<<10), bytes.Repeat([]byte{255}, 256<<10)
	var leases []*eventsLease
	var runs []contract.KVWatchRun
	var keys [][]byte
	for i := 0; i < 3; i++ {
		key := append(nativeKey(t), byte(i))
		keys = append(keys, key)
		seed := nativeDo(t, admin, &pb.PutRequest{Key: key, Value: before})
		l := eventsTestLease(t, s, uint64(i+1))
		leases = append(leases, l)
		w, err := l.StartWatch(context.Background(), contract.KVWatchRequest{Key: key, WatchID: int64(i + 1), StartRevision: seed.Header.Revision, PrevKV: true})
		if err != nil {
			t.Fatal(err)
		}
		runs = append(runs, w)
		received := eventsNativeWait(t, w, func(b contract.KVWatchBatch) bool {
			for _, f := range b.Frames {
				if len(f.Events) == 1 {
					return true
				}
			}
			return false
		})
		found := false
		for _, f := range received.Frames {
			for _, e := range f.Events {
				found = found || bytes.Equal(e.Entry.Value, before)
			}
		}
		if !found {
			t.Fatal("complete initial value not retained")
		}
		if i < 2 {
			nativeDo(t, admin, &pb.PutRequest{Key: key, Value: after})
			changed := eventsNativeWait(t, w, func(b contract.KVWatchBatch) bool {
				for _, f := range b.Frames {
					if len(f.Events) == 1 {
						return true
					}
				}
				return false
			})
			for _, f := range changed.Frames {
				for _, e := range f.Events {
					if !bytes.Equal(e.Entry.Value, after) || e.Previous == nil || !bytes.Equal(e.Previous.Value, before) {
						t.Fatal("original large event/previous value truncated")
					}
				}
			}
		}
	}
	s.mu.Lock()
	charged := s.eventBytes
	s.mu.Unlock()
	if charged <= 24<<20 || charged >= eventSessionBytes {
		t.Fatal("transferred receipt charges were released or exceeded shared budget", charged)
	}
	nativeDo(t, admin, &pb.PutRequest{Key: keys[2], Value: after})
	select {
	case <-leases[2].run.raw.done:
	case <-time.After(5 * time.Second):
		t.Fatal("shared budget did not end original executor")
	}
	overflow := runs[2].Poll()
	if !overflow.Ended || !overflow.Incomplete || overflow.Terminal.Reason != "overflow" {
		t.Fatal("shared budget overflow mislabeled or silently discarded", overflow.Terminal)
	}
	for i := 0; i < 2; i++ {
		if _, ended := leases[i].run.raw.Terminal(); ended || leases[i].CheckKVLease() != nil {
			t.Fatal("one document's overflow stopped a different original stream")
		}
	}
	if err := leases[2].ClearReceipts(); err != nil || leases[2].charged != 0 {
		t.Fatal("explicit overflow receipt disposal failed", err)
	}
	if !bytes.Equal(nativeDo(t, admin, &pb.RangeRequest{Key: keys[2]}).Native().(*pb.RangeResponse).Kvs[0].Value, after) {
		t.Fatal("overflow replayed an original mutation")
	}
}

func TestEtcdNativeEventsActualOriginalWatchLossAndCanceledCreationNeverReconnect(t *testing.T) {
	for _, mode := range []string{"create-loss", "create-cancel", "event-loss"} {
		t.Run(mode, func(t *testing.T) {
			s := eventsNativeSession(t)
			admin := nativeClient(t)
			fault := &watchNativeFault{upstream: admin, mode: mode, entered: make(chan struct{}), done: make(chan struct{})}
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			server := grpc.NewServer(grpc.UnknownServiceHandler(fault.serve))
			go server.Serve(listener)
			defer server.Stop()
			cfg, secret := nativeConfig(t)
			cfg.Endpoint = "http://" + listener.Addr().String()
			cfg.CAFile, cfg.ClientCertFile, cfg.ClientKeyFile, cfg.ServerName = "", "", "", ""
			var opens atomic.Int32
			s.opener = func(ctx context.Context, _ connections.Profile, _ string) (*Client, error) {
				opens.Add(1)
				return Open(ctx, cfg, secret)
			}
			l := eventsTestLease(t, s, 1)
			key := nativeKey(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type outcome struct {
				run contract.KVWatchRun
				err error
			}
			out := make(chan outcome, 1)
			go func() {
				run, err := l.StartWatch(ctx, contract.KVWatchRequest{Key: bytes.Clone(key), WatchID: 73})
				out <- outcome{run, err}
			}()
			var result outcome
			if mode == "event-loss" {
				result = <-out
				if result.err != nil {
					t.Fatal(result.err)
				}
				nativeDo(t, admin, &pb.PutRequest{Key: key, Value: []byte{0, 255, 73}})
			}
			waitSignal(t, fault.entered)
			if mode == "create-cancel" {
				cancel()
			}
			if mode != "event-loss" {
				result = <-out
				if result.err == nil {
					t.Fatal("lost original creation admitted")
				}
			}
			waitSignal(t, fault.done)
			if result.run == nil {
				t.Fatal("original interrupted run lost")
			}
			select {
			case <-l.run.raw.done:
			case <-time.After(5 * time.Second):
				t.Fatal("original receive executor did not settle")
			}
			batch := result.run.Poll()
			want := 0
			if mode == "event-loss" {
				want = 1
			}
			if !batch.Ended || !batch.Incomplete || len(batch.Frames) != want || fault.calls.Load() != 1 || opens.Load() != 1 || fault.receipt.WatchId != 73 || !s.Alive() {
				t.Fatal("original processed stream loss reconnected/substituted receipts")
			}
			if mode == "event-loss" && !bytes.Equal(fault.receipt.Events[0].Kv.Value, []byte{0, 255, 73}) {
				t.Fatal("actual upstream event not observed")
			}
			recovery, err := s.RecoverKVEvents(l)
			if err != nil || recovery.Lease == nil || opens.Load() != 1 || fault.calls.Load() != 1 {
				t.Fatal("explicit event recovery opened/replayed a stream", err)
			}
			defer recovery.Lease.Close()
		})
	}
}

func TestEtcdNativeEventsActualProgressLossKeepsOriginalHistoryOnCancelAndRetirement(t *testing.T) {
	for _, mode := range []string{"deadline", "cancel", "session-close"} {
		t.Run(mode, func(t *testing.T) {
			s := eventsNativeSession(t)
			admin, replacement := nativeClient(t), nativeClient(t)
			key := nativeKey(t)
			first := nativeDo(t, admin, &pb.PutRequest{Key: key, Value: []byte{0, 255, 74}})
			fault := &historyProgressFault{upstream: admin, key: key, entered: make(chan struct{}), done: make(chan struct{})}
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			server := grpc.NewServer(grpc.UnknownServiceHandler(fault.serve))
			go server.Serve(listener)
			defer server.Stop()
			cfg, secret := nativeConfig(t)
			cfg.Endpoint = "http://" + listener.Addr().String()
			cfg.CAFile, cfg.ClientCertFile, cfg.ClientKeyFile, cfg.ServerName = "", "", "", ""
			cfg.RequestTimeout = 1500 * time.Millisecond
			var opens atomic.Int32
			s.opener = func(ctx context.Context, _ connections.Profile, _ string) (*Client, error) {
				opens.Add(1)
				return Open(ctx, cfg, secret)
			}
			fault.ready.Store(true)
			l := eventsTestLease(t, s, 1)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type outcome struct {
				result contract.KVHistoryResult
				err    error
			}
			out := make(chan outcome, 1)
			go func() {
				result, err := l.History(ctx, contract.KVHistoryRequest{Key: bytes.Clone(key), StartRevision: first.Header.Revision, Limit: 100})
				out <- outcome{result, err}
			}()
			waitSignal(t, fault.entered)
			if mode == "cancel" {
				cancel()
			}
			if mode == "session-close" {
				if err = s.Close(); err != nil {
					t.Fatal(err)
				}
			}
			var completed outcome
			select {
			case completed = <-out:
			case <-time.After(5 * time.Second):
				t.Fatal("original history did not acknowledge termination")
			}
			waitSignal(t, fault.done)
			r := completed.result
			last := l.LastHistory()
			if completed.err == nil || r.Complete || !r.Initial.Completed || r.ProgressHeader.Revision != 0 || r.ProgressRequests == 0 || len(r.Events) != 1 || !bytes.Equal(r.Events[0].Entry.Value, []byte{0, 255, 74}) || last == nil || last.Complete || !bytes.Equal(last.Initial.Native, r.Initial.Native) || r.Window.EndRevision != first.Header.Revision {
				t.Fatal("lost progress inferred completeness or erased original history", completed.err)
			}
			if opens.Load() != 1 || fault.rangeCalls.Load() != 1 || fault.watchCalls.Load() != 1 || fault.receipt.WatchId != -1 || fault.receipt.Header.Revision < first.Header.Revision {
				t.Fatal("original history request replayed or upstream proof changed")
			}
			nativeDo(t, replacement, &pb.RangeRequest{Key: key})
		})
	}
}
