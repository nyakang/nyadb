package etcd

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	authpb "go.etcd.io/etcd/api/v3/authpb"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/api/v3/mvccpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func nativeHistoryClient(t *testing.T) *Client {
	t.Helper()
	if os.Getenv("NYADB_ETCD_HISTORY_IT") != "1" {
		t.Skip("requires owned native history fixture")
	}
	return nativeClient(t)
}
func TestEtcdNativeHistoryCapturedWindowDeleteRecreateFutureAndEmptyKeys(t *testing.T) {
	admin := nativeHistoryClient(t)
	c := nativeHistoryClient(t)
	key := nativeKey(t)
	key = append(key, bytes.Repeat([]byte{255}, 4096-len(key))...)
	first := nativeDo(t, admin, &pb.PutRequest{Key: key, Value: []byte{0, 255, 1}})
	nativeDo(t, admin, &pb.PutRequest{Key: key, Value: []byte{0, 255, 2}})
	nativeDo(t, admin, &pb.DeleteRangeRequest{Key: key})
	last := nativeDo(t, admin, &pb.PutRequest{Key: key, Value: []byte{0, 255, 3}})
	future := nativeDo(t, admin, &pb.PutRequest{Key: key, Value: []byte("future")})
	input := HistoryRequest{Key: bytes.Clone(key), StartRevision: first.Header.Revision, EndRevision: last.Header.Revision, Limit: 100}
	result, err := c.History(context.Background(), input)
	if err != nil || !result.Complete || result.Truncated || len(result.Events) != 4 || result.Window.EndRevision != last.Header.Revision || result.ProgressHeader.Revision < last.Header.Revision {
		t.Fatal("original fixed history window incomplete/expanded", err, result.Complete, len(result.Events))
	}
	if result.Events[2].Type != mvccpb.DELETE || !bytes.Equal(result.Events[2].PrevKv.Value, []byte{0, 255, 2}) || result.Events[3].Kv.Version != 1 || result.Events[3].Kv.CreateRevision != last.Header.Revision {
		t.Fatal("delete/recreate metadata was flattened")
	}
	sawFuture := false
	for _, frame := range result.Frames {
		for _, e := range frame.Events {
			if e.Kv.ModRevision == future.Header.Revision {
				sawFuture = true
			}
		}
	}
	if !sawFuture {
		t.Fatal("complete native receipt lost later events instead of projecting the window")
	}
	for _, e := range result.Events {
		if e.Kv.ModRevision > last.Header.Revision {
			t.Fatal("future event entered original history projection")
		}
	}
	input.Key[0] ^= 255
	if !bytes.Equal(result.Authored.Key, key) || !bytes.Equal(result.Window.Key, key) {
		t.Fatal("original history key aliases authored input")
	}
	empty := nativeKey(t)
	result, err = c.History(context.Background(), HistoryRequest{Key: empty, Limit: 100})
	if err != nil || !result.Complete || len(result.Events) != 0 || result.ProgressHeader.Revision < result.Window.EndRevision {
		t.Fatal("empty key was not proven by original progress", err)
	}
	progress := false
	for _, f := range result.Frames {
		progress = progress || f.Progress
	}
	if !progress || result.Terminal.Reason != "stopped" {
		t.Fatal("created receipt or timeout substituted for completion")
	}
}
func TestEtcdNativeHistoryNativeReceiptsKeepCompleteWindowBeyondProjectionLimit(t *testing.T) {
	admin := nativeHistoryClient(t)
	c := nativeHistoryClient(t)
	key := nativeKey(t)
	first := nativeDo(t, admin, &pb.PutRequest{Key: key, Value: []byte("one")})
	nativeDo(t, admin, &pb.PutRequest{Key: key, Value: []byte("two")})
	last := nativeDo(t, admin, &pb.PutRequest{Key: key, Value: []byte("three")})
	result, err := c.History(context.Background(), HistoryRequest{Key: key, StartRevision: first.Header.Revision, EndRevision: last.Header.Revision, Limit: 1})
	if err != nil || !result.Complete || !result.Truncated || len(result.Events) != 1 || !bytes.Equal(result.Events[0].Kv.Value, []byte("three")) {
		t.Fatal("bounded projection did not preserve newest original-window event", err)
	}
	total := 0
	for _, f := range result.Frames {
		for _, event := range f.Events {
			if event.Kv.ModRevision <= last.Header.Revision {
				total++
			}
		}
	}
	if total != 3 {
		t.Fatal("projection limit evicted complete original native frames")
	}
}
func TestEtcdNativeHistoryActualCompactionDoesNotMoveRequestedEndRevision(t *testing.T) {
	admin := nativeHistoryClient(t)
	c := nativeHistoryClient(t)
	key := nativeKey(t)
	first := nativeDo(t, admin, &pb.PutRequest{Key: key, Value: []byte("before")})
	last := nativeDo(t, admin, &pb.PutRequest{Key: key, Value: []byte("after")})
	nativeDo(t, admin, &pb.CompactionRequest{Revision: last.Header.Revision})
	result, err := c.History(context.Background(), HistoryRequest{Key: key, StartRevision: first.Header.Revision, Limit: 100})
	if err == nil || result.Complete || result.Terminal.Reason != "compacted" || len(result.Frames) != 2 || result.Frames[1].CompactRevision != last.Header.Revision || result.Frames[1].Header.Revision != 0 {
		t.Fatal("actual native compaction receipt lost/replaced", err, result.Terminal.Reason)
	}
	result, err = c.History(context.Background(), HistoryRequest{Key: key, StartRevision: first.Header.Revision, EndRevision: first.Header.Revision, Limit: 100})
	if err == nil || result.Complete || len(result.Frames) != 0 || result.Initial.Code != codes.OutOfRange || result.Window.EndRevision != first.Header.Revision {
		t.Fatal("compacted exact range moved to current revision", err)
	}
	if errors.Is(err, ErrRetired) {
		t.Fatal("definite compaction unexpectedly retired unrelated original client")
	}
}

func TestEtcdNativeHistoryOriginalReadOnlyIdentityAndDeniedRange(t *testing.T) {
	_ = nativeHistoryClient(t)
	admin := accessNativeAdmin(t)
	key := nativeKey(t)
	first := nativeDo(t, admin, &pb.PutRequest{Key: key, Value: []byte{0, 255, 88}})
	user := "history-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	accessNativeUser(t, admin, user, user, []*authpb.Permission{{PermType: authpb.READ, Key: key}})
	cfg, _ := nativeConfig(t)
	cfg.Auth = "basic"
	cfg.Username = user
	cfg.ProbeKey = key
	c, err := Open(context.Background(), cfg, os.Getenv("NYADB_ETCD_SECRET"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	result, err := c.History(context.Background(), HistoryRequest{Key: key, StartRevision: first.Header.Revision, Limit: 100})
	if err != nil || !result.Complete || len(result.Events) != 1 || !bytes.Equal(result.Events[0].Kv.Value, []byte{0, 255, 88}) {
		t.Fatal("read-only original history identity rejected", err)
	}
	forbidden := []byte("/forbidden/history")
	result, err = c.History(context.Background(), HistoryRequest{Key: forbidden, Limit: 100})
	if status.Code(err) != codes.PermissionDenied || result.Complete || len(result.Frames) != 0 || result.Initial.Code != codes.PermissionDenied || !c.Alive() || !bytes.Equal(result.Window.Key, forbidden) {
		t.Fatal("denied original range replaced/retried", err)
	}
}

type historyProgressFault struct {
	upstream               *Client
	key                    []byte
	rangeCalls, watchCalls atomic.Int32
	ready                  atomic.Bool
	entered, done          chan struct{}
	receipt                *pb.WatchResponse
}

func (f *historyProgressFault) serve(_ any, downstream grpc.ServerStream) error {
	method, _ := grpc.MethodFromServerStream(downstream)
	if method == "/etcdserverpb.KV/Range" {
		request := &pb.RangeRequest{}
		if err := downstream.RecvMsg(request); err != nil {
			return err
		}
		if f.ready.Load() && bytes.Equal(request.Key, f.key) {
			f.rangeCalls.Add(1)
		}
		reply, err := pb.NewKVClient(f.upstream.conn).Range(f.upstream.authContext(downstream.Context()), request)
		if err != nil {
			return err
		}
		return downstream.SendMsg(reply)
	}
	if method != "/etcdserverpb.Watch/Watch" {
		return (&nativeFault{upstream: f.upstream}).serve(nil, downstream)
	}
	defer close(f.done)
	request := &pb.WatchRequest{}
	if err := downstream.RecvMsg(request); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(downstream.Context())
	stream, err := pb.NewWatchClient(f.upstream.conn).Watch(f.upstream.authContext(ctx))
	if err != nil {
		cancel()
		return err
	}
	f.watchCalls.Add(1)
	if err = stream.Send(request); err != nil {
		cancel()
		return err
	}
	senderDone := make(chan struct{})
	go func() {
		defer close(senderDone)
		for {
			next := &pb.WatchRequest{}
			if downstream.RecvMsg(next) != nil {
				return
			}
			if stream.Send(next) != nil {
				return
			}
		}
	}()
	defer func() { cancel(); <-senderDone }()
	for {
		reply, err := stream.Recv()
		if err != nil {
			return err
		}
		if !reply.Created && !reply.Canceled && len(reply.Events) == 0 && reply.WatchId == -1 {
			// Capture an actual upstream progress acknowledgement, then hold it.
			f.receipt = proto.Clone(reply).(*pb.WatchResponse)
			close(f.entered)
			<-ctx.Done()
			return ctx.Err()
		}
		if err = downstream.SendMsg(reply); err != nil {
			return err
		}
	}
}
func TestEtcdNativeHistoryActualProgressLossTimeoutCancelAndOriginalClose(t *testing.T) {
	for _, mode := range []string{"deadline", "cancel", "close"} {
		t.Run(mode, func(t *testing.T) {
			upstream := nativeHistoryClient(t)
			admin := nativeHistoryClient(t)
			replacement := nativeHistoryClient(t)
			key := nativeKey(t)
			first := nativeDo(t, admin, &pb.PutRequest{Key: key, Value: []byte("original")})
			fault := &historyProgressFault{upstream: upstream, key: key, entered: make(chan struct{}), done: make(chan struct{})}
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			server := grpc.NewServer(grpc.UnknownServiceHandler(fault.serve))
			go server.Serve(listener)
			defer server.Stop()
			cfg, secret := nativeConfig(t)
			cfg.Endpoint = "http://" + listener.Addr().String()
			cfg.CAFile = ""
			cfg.ClientCertFile = ""
			cfg.ClientKeyFile = ""
			cfg.ServerName = ""
			cfg.RequestTimeout = 1500 * time.Millisecond
			c, err := Open(context.Background(), cfg, secret)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			fault.ready.Store(true)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type outcome struct {
				result HistoryResult
				err    error
			}
			out := make(chan outcome, 1)
			go func() {
				r, e := c.History(ctx, HistoryRequest{Key: bytes.Clone(key), StartRevision: first.Header.Revision, Limit: 100})
				out <- outcome{r, e}
			}()
			waitSignal(t, fault.entered)
			if mode == "cancel" {
				cancel()
			}
			if mode == "close" {
				if err = c.Close(); err != nil {
					t.Fatal(err)
				}
			}
			var completed outcome
			select {
			case completed = <-out:
			case <-time.After(5 * time.Second):
				t.Fatal("original history executor did not acknowledge termination")
			}
			waitSignal(t, fault.done)
			r := completed.result
			if completed.err == nil || r.Complete || len(r.Events) != 1 || !bytes.Equal(r.Events[0].Kv.Value, []byte("original")) || !r.Initial.Completed || r.ProgressHeader.Revision != 0 || r.Window.EndRevision != first.Header.Revision {
				t.Fatal("lost progress/timeout replaced original history evidence", completed.err, r.Complete, len(r.Events))
			}
			for _, frame := range r.Frames {
				if frame.Progress {
					t.Fatal("unreceived native progress fabricated")
				}
			}
			if fault.rangeCalls.Load() != 1 || fault.watchCalls.Load() != 1 || fault.receipt.Header.Revision < first.Header.Revision || fault.receipt.WatchId != -1 {
				t.Fatal("history original range/watch replayed or upstream proof fabricated")
			}
			if c.Alive() {
				t.Fatal("interrupted original history channel silently recovered")
			}
			nativeDo(t, replacement, &pb.RangeRequest{Key: key})
		})
	}
}
func TestEtcdNativeHistoryCompleteLargeValuesAndExplicitNativeCaptureBounds(t *testing.T) {
	for _, size := range []int{256 << 10, 1 << 20} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			admin := nativeHistoryClient(t)
			c := nativeHistoryClient(t)
			key := nativeKey(t)
			before := bytes.Repeat([]byte{0}, size)
			after := bytes.Repeat([]byte{255}, size)
			first := nativeDo(t, admin, &pb.PutRequest{Key: key, Value: before})
			last := nativeDo(t, admin, &pb.PutRequest{Key: key, Value: after})
			result, err := c.History(context.Background(), HistoryRequest{Key: key, StartRevision: first.Header.Revision, EndRevision: last.Header.Revision, Limit: 100})
			if size == 256<<10 {
				if err != nil || !result.Complete || result.CaptureIncomplete || len(result.Events) != 2 || !bytes.Equal(result.Events[1].Kv.Value, after) || !bytes.Equal(result.Events[1].PrevKv.Value, before) {
					t.Fatal("complete bounded historical values changed/truncated", err)
				}
			} else if !errors.Is(err, ErrBounds) || result.Complete || !result.CaptureIncomplete || !result.Initial.Completed || result.Terminal.Reason != "overflow" {
				t.Fatal("over-budget native history claimed completion or lost original range", err, result.Terminal.Reason)
			}
			if !bytes.Equal(nativeDo(t, admin, &pb.RangeRequest{Key: key}).Native().(*pb.RangeResponse).Kvs[0].Value, after) {
				t.Fatal("history replayed a mutation")
			}
		})
	}
}

// Route the fixed Range and historical Watch to the actual owned upstream.
// Drop only a response that upstream has already produced, never a fake reply.
type historyReceiptFault struct {
	watch      *watchNativeFault
	key        []byte
	mode       string
	ready      atomic.Bool
	rangeCalls atomic.Int32
	rangeReply *pb.RangeResponse
}

func (f *historyReceiptFault) serve(_ any, downstream grpc.ServerStream) error {
	method, _ := grpc.MethodFromServerStream(downstream)
	if method != "/etcdserverpb.KV/Range" {
		return f.watch.serve(nil, downstream)
	}
	r := &pb.RangeRequest{}
	if err := downstream.RecvMsg(r); err != nil {
		return err
	}
	reply, err := pb.NewKVClient(f.watch.upstream.conn).Range(f.watch.upstream.authContext(downstream.Context()), r)
	if err != nil {
		return err
	}
	if f.ready.Load() && bytes.Equal(r.Key, f.key) {
		f.rangeCalls.Add(1)
		f.rangeReply = proto.Clone(reply).(*pb.RangeResponse)
		if f.mode == "range-loss" {
			close(f.watch.entered)
			close(f.watch.done)
			return status.Error(codes.Unavailable, "owned actual history Range receipt lost")
		}
	}
	return downstream.SendMsg(reply)
}

func TestEtcdNativeHistoryActualRangeCreationAndEventReceiptLossNeverReplay(t *testing.T) {
	for _, mode := range []string{"range-loss", "create-loss", "event-loss"} {
		t.Run(mode, func(t *testing.T) {
			upstream := nativeHistoryClient(t)
			admin := nativeHistoryClient(t)
			replacement := nativeHistoryClient(t)
			key := nativeKey(t)
			first := nativeDo(t, admin, &pb.PutRequest{Key: key, Value: []byte{0, 255, 91}})
			watch := &watchNativeFault{upstream: upstream, mode: mode, entered: make(chan struct{}), done: make(chan struct{})}
			fault := &historyReceiptFault{watch: watch, key: key, mode: mode}
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
			c, err := Open(context.Background(), cfg, secret)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			fault.ready.Store(true)
			r, err := c.History(context.Background(), HistoryRequest{Key: key, StartRevision: first.Header.Revision, EndRevision: first.Header.Revision, Limit: 100})
			waitSignal(t, watch.entered)
			waitSignal(t, watch.done)
			if err == nil || r.Complete || len(r.Events) != 0 || r.ProgressHeader.Revision != 0 || r.Window.EndRevision != first.Header.Revision || c.Alive() || fault.rangeCalls.Load() != 1 || fault.rangeReply.Header.Revision < first.Header.Revision {
				t.Fatal("lost original history response was replayed or inferred", err)
			}
			wantCalls, wantFrames := int32(1), 0
			if mode == "range-loss" {
				wantCalls = 0
				if r.Initial.Completed || r.Initial.Native() != nil {
					t.Fatal("lost fixed Range published")
				}
			} else if !r.Initial.Completed || r.Initial.Native() == nil {
				t.Fatal("acknowledged original fixed Range lost")
			}
			if mode == "event-loss" {
				wantFrames = 1
				if len(watch.receipt.Events) != 1 || !bytes.Equal(watch.receipt.Events[0].Kv.Value, []byte{0, 255, 91}) {
					t.Fatal("actual original history event not captured upstream")
				}
			}
			if watch.calls.Load() != wantCalls || len(r.Frames) != wantFrames {
				t.Fatal("original historical Watch recreated or lost response published")
			}
			nativeDo(t, replacement, &pb.RangeRequest{Key: key})
		})
	}
}
