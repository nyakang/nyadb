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

	authpb "go.etcd.io/etcd/api/v3/authpb"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/api/v3/mvccpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func nativeWatchClient(t *testing.T) *Client {
	t.Helper()
	if os.Getenv("NYADB_ETCD_WATCH_IT") != "1" {
		t.Skip("requires owned raw Watch fixture")
	}
	return nativeClient(t)
}
func watchNativeWait(t *testing.T, w *WatchRun, predicate func([]WatchFrame) bool) []WatchFrame {
	t.Helper()
	var frames []WatchFrame
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		frames = append(frames, w.Poll()...)
		if predicate(frames) {
			return frames
		}
		if terminal, ended := w.Terminal(); ended {
			t.Fatal("original watch ended before observation", terminal.Reason, terminal.Cause)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("original watch did not produce required observation")
	return nil
}
func TestEtcdNativeWatchOriginalBinaryEventsCompleteReceiptsProgressAndStop(t *testing.T) {
	admin := nativeWatchClient(t)
	c := nativeWatchClient(t)
	prefix := nativeKey(t)
	a := append(bytes.Clone(prefix), 0, 255)
	a = append(a, bytes.Repeat([]byte{0}, 4096-len(a))...)
	b := append(bytes.Clone(prefix), 1, 255)
	first := nativeDo(t, admin, &pb.PutRequest{Key: a, Value: []byte{0, 255}})
	request := PrefixRange(prefix, 1, 0)
	w, err := c.StartWatch(context.Background(), &pb.WatchCreateRequest{Key: request.Key, RangeEnd: request.RangeEnd, WatchId: 42, StartRevision: first.Header.Revision, PrevKv: true, ProgressNotify: true})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	snapshot := w.Request()
	snapshot.Key[0] = 255
	if !bytes.Equal(w.Request().Key, prefix) {
		t.Fatal("request snapshot aliases original")
	}
	if _, err = c.StartWatch(context.Background(), w.Request()); !errors.Is(err, ErrBusy) {
		t.Fatal("parallel replacement stream admitted", err)
	}
	txn := nativeDo(t, admin, &pb.TxnRequest{Success: []*pb.RequestOp{putOp(a, []byte{1, 0, 255}, 0), putOp(b, []byte("b"), 0)}})
	deleted := nativeDo(t, admin, &pb.DeleteRangeRequest{Key: a, PrevKv: true})
	frames := watchNativeWait(t, w, func(fs []WatchFrame) bool {
		n := 0
		for _, f := range fs {
			n += len(f.Events)
		}
		return n == 4
	})
	var events []*mvccpb.Event
	for _, frame := range frames {
		original := &pb.WatchResponse{}
		err = proto.Unmarshal(frame.Native, original)
		if err != nil {
			t.Fatal("native receipt decode", err)
		}
		if len(original.Events) != len(frame.Events) {
			t.Fatal("receipt projection dropped original events")
		}
		for i, event := range frame.Events {
			if !proto.Equal(original.Events[i], event) {
				t.Fatal("native projection changed event fields")
			}
		}
		events = append(events, frame.Events...)
	}
	if len(events) != 4 || events[1].Kv.ModRevision != txn.Header.Revision || events[2].Kv.ModRevision != txn.Header.Revision || events[3].Type != mvccpb.DELETE || events[3].Kv.ModRevision != deleted.Header.Revision || !bytes.Equal(events[3].PrevKv.Value, []byte{1, 0, 255}) {
		t.Fatal("original binary/transaction/delete history changed")
	}
	if err = w.Progress(); err != nil {
		t.Fatal(err)
	}
	watchNativeWait(t, w, func(fs []WatchFrame) bool {
		for _, f := range fs {
			if f.Progress && f.WatchID == -1 && f.Header.Revision >= deleted.Header.Revision {
				return true
			}
		}
		return false
	})
	if err = w.Stop(context.Background()); err != nil {
		t.Fatal("original stop acknowledgement", err)
	}
	terminal, ended := w.Terminal()
	if !ended || terminal.Reason != "stopped" {
		t.Fatal("stop did not wait for original terminal")
	}
	terminalFrames := w.Poll()
	if len(terminalFrames) != 1 || !terminalFrames[0].Canceled {
		t.Fatal("actual original cancel receipt lost")
	}
	nativeDo(t, c, &pb.RangeRequest{Key: b})
}
func TestEtcdNativeWatchOriginalCompactionAndRestrictedCreationReceipts(t *testing.T) {
	admin := nativeWatchClient(t)
	c := nativeWatchClient(t)
	key := nativeKey(t)
	first := nativeDo(t, admin, &pb.PutRequest{Key: key, Value: []byte("before")})
	last := nativeDo(t, admin, &pb.PutRequest{Key: key, Value: []byte("after")})
	_, err := pb.NewKVClient(admin.conn).Compact(admin.authContext(context.Background()), &pb.CompactionRequest{Revision: last.Header.Revision})
	if err != nil {
		t.Fatal(err)
	}
	w, err := c.StartWatch(context.Background(), &pb.WatchCreateRequest{Key: key, WatchId: 99, StartRevision: first.Header.Revision})
	if err != nil {
		t.Fatal("created receipt should precede compaction terminal", err)
	}
	defer w.Close()
	select {
	case <-w.done:
	case <-time.After(5 * time.Second):
		t.Fatal("original compaction not terminal")
	}
	terminal, _ := w.Terminal()
	frames := w.Poll()
	if terminal.Reason != "compacted" || len(frames) != 2 || frames[1].CompactRevision != last.Header.Revision || frames[1].Header.Revision != 0 || frames[1].Header.ClusterID != c.identity.ClusterID || frames[1].Header.MemberID != c.identity.MemberID {
		t.Fatal("actual zero-revision compacted receipt changed", terminal, frames)
	}
	admin = accessNativeAdmin(t)
	cfg, secret := nativeConfig(t)
	cfg.Auth = "basic"
	cfg.Username = "restricted"
	cfg.ProbeKey = []byte("/allowed/key")
	// Anonymous access helpers create only root; create this test's restricted identity.
	if os.Getenv("NYADB_ETCD_MODE") == "anonymous" {
		auth := pb.NewAuthClient(admin.conn)
		ctx := admin.authContext(context.Background())
		_, err = auth.UserAdd(ctx, &pb.AuthUserAddRequest{Name: "restricted", Password: os.Getenv("NYADB_ETCD_SECRET")})
		if err != nil {
			t.Fatal(err)
		}
		_, err = auth.RoleAdd(ctx, &pb.AuthRoleAddRequest{Name: "restricted"})
		if err != nil {
			t.Fatal(err)
		}
		_, err = auth.RoleGrantPermission(ctx, &pb.AuthRoleGrantPermissionRequest{Name: "restricted", Perm: &authpb.Permission{PermType: authpb.READ, Key: []byte("/allowed/"), RangeEnd: []byte("/allowed0")}})
		if err != nil {
			t.Fatal(err)
		}
		_, err = auth.UserGrantRole(ctx, &pb.AuthUserGrantRoleRequest{User: "restricted", Role: "restricted"})
		if err != nil {
			t.Fatal(err)
		}
	}
	secret = os.Getenv("NYADB_ETCD_SECRET")
	limited, err := Open(context.Background(), cfg, secret)
	if err != nil {
		t.Fatal(err)
	}
	defer limited.Close()
	denied, err := limited.StartWatch(context.Background(), &pb.WatchCreateRequest{Key: []byte("/forbidden/"), WatchId: 111})
	if err == nil || denied == nil {
		t.Fatal("server denied creation admitted", err)
	}
	defer denied.Close()
	<-denied.done
	frames = denied.Poll()
	if len(frames) != 1 || !frames[0].Created || !frames[0].Canceled || frames[0].WatchID != -1 || len(frames[0].Events) != 0 {
		t.Fatal("actual denial receipt replaced/lost")
	}
}
func TestEtcdNativeWatchOriginalCancellationCloseAndBufferBounds(t *testing.T) {
	for _, mode := range []string{"cancel", "close", "overflow"} {
		t.Run(mode, func(t *testing.T) {
			admin := nativeWatchClient(t)
			c := nativeWatchClient(t)
			replacement := nativeWatchClient(t)
			key := nativeKey(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			w, err := c.StartWatch(ctx, &pb.WatchCreateRequest{Key: key, WatchId: 7, PrevKv: true})
			if err != nil {
				t.Fatal(err)
			}
			defer w.Close()
			switch mode {
			case "cancel":
				cancel()
			case "close":
				if err = c.Close(); err != nil {
					t.Fatal(err)
				}
			case "overflow":
				nativeDo(t, admin, &pb.PutRequest{Key: key, Value: bytes.Repeat([]byte{0}, 1<<20)})
				watchNativeWait(t, w, func(fs []WatchFrame) bool {
					for _, f := range fs {
						if len(f.Events) == 1 {
							return true
						}
					}
					return false
				})
				nativeDo(t, admin, &pb.PutRequest{Key: key, Value: bytes.Repeat([]byte{255}, 1<<20)})
			}
			select {
			case <-w.done:
			case <-time.After(5 * time.Second):
				t.Fatal("original executor did not acknowledge termination")
			}
			terminal, _ := w.Terminal()
			if mode == "overflow" && (terminal.Reason != "overflow" || !errors.Is(terminal.Cause, ErrBounds)) {
				t.Fatal("oversized complete frame was truncated or mislabeled", terminal)
			}
			nativeDo(t, replacement, &pb.RangeRequest{Key: key})
		})
	}
}

// Observe the actual upstream create/event frame before dropping or holding
// that original response. The proxy never synthesizes successful Watch data.
type watchNativeFault struct {
	upstream      *Client
	mode          string
	calls         atomic.Int32
	entered, done chan struct{}
	receipt       *pb.WatchResponse
}

func (f *watchNativeFault) serve(_ any, downstream grpc.ServerStream) error {
	method, _ := grpc.MethodFromServerStream(downstream)
	if method != "/etcdserverpb.Watch/Watch" {
		return (&nativeFault{upstream: f.upstream}).serve(nil, downstream)
	}
	defer close(f.done)
	request := &pb.WatchRequest{}
	if err := downstream.RecvMsg(request); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(downstream.Context())
	defer cancel()
	stream, err := pb.NewWatchClient(f.upstream.conn).Watch(f.upstream.authContext(ctx))
	if err != nil {
		return err
	}
	f.calls.Add(1)
	if err = stream.Send(request); err != nil {
		return err
	}
	receipt, err := stream.Recv()
	if err != nil {
		return err
	}
	if !receipt.Created || receipt.Canceled {
		return ErrProtocol
	}
	if f.mode == "event-loss" {
		if err = downstream.SendMsg(receipt); err != nil {
			return err
		}
		receipt, err = stream.Recv()
		if err != nil {
			return err
		}
		if len(receipt.Events) != 1 {
			return ErrProtocol
		}
	}
	f.receipt = proto.Clone(receipt).(*pb.WatchResponse)
	close(f.entered)
	if f.mode == "create-cancel" {
		<-ctx.Done()
		return ctx.Err()
	}
	return status.Error(codes.Unavailable, "owned actual Watch receipt lost")
}
func TestEtcdNativeWatchActualOriginalCreationAndEventReceiptLossNeverReconnect(t *testing.T) {
	for _, mode := range []string{"create-loss", "create-cancel", "event-loss"} {
		t.Run(mode, func(t *testing.T) {
			upstream := nativeWatchClient(t)
			admin := nativeWatchClient(t)
			replacement := nativeWatchClient(t)
			fault := &watchNativeFault{upstream: upstream, mode: mode, entered: make(chan struct{}), done: make(chan struct{})}
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
			c, err := Open(context.Background(), cfg, secret)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			key := nativeKey(t)
			type opened struct {
				run *WatchRun
				err error
			}
			out := make(chan opened, 1)
			go func() {
				w, err := c.StartWatch(ctx, &pb.WatchCreateRequest{Key: key, WatchId: 73})
				out <- opened{w, err}
			}()
			var original *WatchRun
			if mode == "event-loss" {
				result := <-out
				if result.err != nil {
					t.Fatal(result.err)
				}
				original = result.run
				nativeDo(t, admin, &pb.PutRequest{Key: key, Value: []byte{0, 255, 73}})
			}
			select {
			case <-fault.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("actual original upstream receipt not captured")
			}
			if mode == "create-cancel" {
				cancel()
			}
			if mode != "event-loss" {
				result := <-out
				if result.err == nil {
					t.Fatal("lost creation admitted")
				}
				original = result.run
			}
			if original == nil {
				t.Fatal("original interrupted run unavailable")
			}
			defer original.Close()
			select {
			case <-original.done:
			case <-time.After(5 * time.Second):
				t.Fatal("original driver did not acknowledge receipt loss/cancel")
			}
			select {
			case <-fault.done:
			case <-time.After(5 * time.Second):
				t.Fatal("original upstream handler did not settle")
			}
			if fault.calls.Load() != 1 || fault.receipt.WatchId != 73 || mode == "event-loss" && !bytes.Equal(fault.receipt.Events[0].Kv.Value, []byte{0, 255, 73}) {
				t.Fatal("original stream recreated or receipt fabricated")
			}
			expectedFrames := 0
			if mode == "event-loss" {
				expectedFrames = 1
			}
			if frames := original.Poll(); len(frames) != expectedFrames {
				t.Fatal("lost receipt was published or prior receipt lost")
			}
			if _, err = c.StartWatch(context.Background(), original.Request()); !errors.Is(err, ErrRetired) {
				t.Fatal("interrupted original channel silently recovered")
			}
			nativeDo(t, replacement, &pb.RangeRequest{Key: key})
		})
	}
}
