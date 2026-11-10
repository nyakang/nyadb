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
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

func nativeLeaseBrowserClient(t *testing.T) *Client {
	t.Helper()
	if os.Getenv("NYADB_ETCD_LEASE_BROWSER_IT") != "1" {
		t.Skip("requires owned original lease-browser fixture")
	}
	return nativeClient(t)
}

func TestEtcdNativeLeaseBrowserActualPermissionLossKeepsIndependentReads(t *testing.T) {
	_ = nativeLeaseBrowserClient(t)
	admin := accessNativeAdmin(t)
	key := nativeKey(t)
	user := "lease-read-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	accessNativeUser(t, admin, user, user, []*authpb.Permission{{PermType: authpb.READWRITE, Key: key}})
	cfg, _ := nativeConfig(t)
	cfg.Auth, cfg.Username, cfg.ProbeKey = "basic", user, key
	c, err := Open(context.Background(), cfg, os.Getenv("NYADB_ETCD_SECRET"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	grant := nativeDo(t, admin, &pb.LeaseGrantRequest{TTL: 60}).Native().(*pb.LeaseGrantResponse)
	t.Cleanup(func() { nativeDo(t, admin, &pb.LeaseRevokeRequest{ID: grant.ID}) })
	nativeDo(t, admin, &pb.PutRequest{Key: key, Value: []byte("original"), Lease: grant.ID})
	r, err := c.ObserveLease(context.Background(), grant.ID, true)
	if err != nil || !r.Completed || len(r.Native().(*pb.LeaseTimeToLiveResponse).Keys) != 1 {
		t.Fatal("original granted observation failed", err)
	}
	if _, err := pb.NewAuthClient(admin.conn).RoleRevokePermission(admin.authContext(context.Background()), &pb.AuthRoleRevokePermissionRequest{Role: user, Key: key}); err != nil {
		t.Fatal(err)
	}
	r, err = c.ObserveLease(context.Background(), grant.ID, true)
	if status.Code(err) != codes.PermissionDenied || r.Completed || r.WriteOutcomeUnknown || !c.Alive() {
		t.Fatal("revoked native lease read bypassed permissions or became a write", err)
	}
	independent, err := admin.ObserveLease(context.Background(), grant.ID, true)
	if err != nil || !independent.Completed || len(independent.Native().(*pb.LeaseTimeToLiveResponse).Keys) != 1 {
		t.Fatal("denial retired independent executor", err)
	}
}

type leaseBrowserFault struct {
	upstream                    *Client
	method, mode                string
	after                       int32
	listCalls, ttlCalls, writes atomic.Int32
	entered, done               chan struct{}
	receipt                     proto.Message
}

func (f *leaseBrowserFault) serve(_ any, stream grpc.ServerStream) error {
	method, _ := grpc.MethodFromServerStream(stream)
	var request, response proto.Message
	var call int32
	switch method {
	case "/etcdserverpb.Lease/LeaseLeases":
		request, response = &pb.LeaseLeasesRequest{}, &pb.LeaseLeasesResponse{}
		call = f.listCalls.Add(1)
	case "/etcdserverpb.Lease/LeaseTimeToLive":
		request, response = &pb.LeaseTimeToLiveRequest{}, &pb.LeaseTimeToLiveResponse{}
		call = f.ttlCalls.Add(1)
	default:
		if method == "/etcdserverpb.Lease/LeaseGrant" || method == "/etcdserverpb.Lease/LeaseRevoke" || method == "/etcdserverpb.Lease/LeaseKeepAlive" {
			f.writes.Add(1)
		}
		return (&nativeFault{upstream: f.upstream}).serve(nil, stream)
	}
	if err := stream.RecvMsg(request); err != nil {
		return err
	}
	if err := f.upstream.conn.Invoke(f.upstream.authContext(stream.Context()), method, request, response); err != nil {
		return err
	}
	if f.mode == "aggregate-budget" && method == "/etcdserverpb.Lease/LeaseTimeToLive" {
		unknown := protowire.AppendTag(nil, 100, protowire.BytesType)
		unknown = protowire.AppendBytes(unknown, make([]byte, 8<<20))
		response.ProtoReflect().SetUnknown(unknown)
	}
	if method == f.method && call == f.after {
		f.receipt = proto.Clone(response)
		close(f.entered)
		switch f.mode {
		case "lost":
			close(f.done)
			return status.Error(codes.Unavailable, "actual original lease read receipt discarded")
		case "cancel", "close":
			<-stream.Context().Done()
			close(f.done)
			return stream.Context().Err()
		case "foreign-id":
			response.(*pb.LeaseTimeToLiveResponse).ID++
		case "duplicate-id":
			v := response.(*pb.LeaseLeasesResponse)
			v.Leases = append(v.Leases, proto.Clone(v.Leases[0]).(*pb.LeaseStatus))
		case "oversized-list":
			v := response.(*pb.LeaseLeasesResponse)
			v.Leases = make([]*pb.LeaseStatus, 10001)
			for i := range v.Leases {
				v.Leases[i] = &pb.LeaseStatus{ID: int64(i + 1)}
			}
		case "unknown-fields":
			response.ProtoReflect().SetUnknown([]byte{0xa0, 6, 7})
		}
		close(f.done)
	}
	return stream.SendMsg(response)
}

func TestEtcdNativeLeaseBrowserAggregateBudgetKeepsCompleteOriginalReceipts(t *testing.T) {
	admin := nativeLeaseBrowserClient(t)
	base := int64(9007199254744992)
	for i := int64(1); i <= 5; i++ {
		id := base + i
		nativeDo(t, admin, &pb.LeaseGrantRequest{TTL: 60, ID: id})
		t.Cleanup(func() { nativeDo(t, admin, &pb.LeaseRevokeRequest{ID: id}) })
	}
	c, f := leaseBrowserProxy(t, admin, "", "aggregate-budget", 0)
	page, err := c.BrowseLeases(context.Background(), LeasePageRequest{Limit: 5, After: &base})
	if !errors.Is(err, ErrBounds) || page.Complete || page.NextAfter != nil || len(page.Rows) != 2 || !page.List.Completed || !c.Alive() || f.listCalls.Load() != 1 || f.ttlCalls.Load() != 2 || f.writes.Load() != 0 {
		t.Fatal("aggregate budget evicted receipts, read again or retired a healthy executor", err, len(page.Rows), f.ttlCalls.Load())
	}
	captured := proto.Size(page.List.native)
	for i, row := range page.Rows {
		v := row.Receipt.Native().(*pb.LeaseTimeToLiveResponse)
		if !row.Receipt.Completed || row.Receipt.WriteOutcomeUnknown || v.ID != base+int64(i)+1 || len(v.ProtoReflect().GetUnknown()) < 8<<20 {
			t.Fatal("complete original budget receipt was clipped or replaced")
		}
		captured += proto.Size(v)
	}
	if captured > leaseBrowserByteLimit {
		t.Fatal("aggregate native budget exceeded")
	}
	independent, err := admin.ObserveLease(context.Background(), base+1, false)
	if err != nil || !independent.Completed {
		t.Fatal("budget changed the original lease or an independent read", err)
	}
}

func leaseBrowserProxy(t *testing.T, upstream *Client, method, mode string, after int32) (*Client, *leaseBrowserFault) {
	t.Helper()
	cfg, secret := nativeConfig(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &leaseBrowserFault{upstream: upstream, method: method, mode: mode, after: after, entered: make(chan struct{}), done: make(chan struct{})}
	server := grpc.NewServer(grpc.UnknownServiceHandler(f.serve))
	go server.Serve(listener)
	t.Cleanup(func() { server.Stop(); listener.Close() })
	cfg.Endpoint = "http://" + listener.Addr().String()
	cfg.CAFile, cfg.ClientCertFile, cfg.ClientKeyFile, cfg.ServerName = "", "", "", ""
	c, err := Open(context.Background(), cfg, secret)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c, f
}

func TestEtcdNativeLeaseBrowserActualListWindowsAndCompleteAttachedKeys(t *testing.T) {
	admin := nativeLeaseBrowserClient(t)
	c, f := leaseBrowserProxy(t, admin, "", "", 0)
	ids := []int64{9007199254741993, 9007199254741995, 9007199254741994}
	live := map[int64]bool{}
	for _, id := range ids {
		nativeDo(t, admin, &pb.LeaseGrantRequest{TTL: 60, ID: id})
		live[id] = true
		t.Cleanup(func() {
			if live[id] {
				nativeDo(t, admin, &pb.LeaseRevokeRequest{ID: id})
			}
		})
	}
	base := ids[0] - 1
	first, err := c.BrowseLeases(context.Background(), LeasePageRequest{Limit: 2, After: &base})
	if err != nil || !first.Complete || !first.List.Completed || !first.More || len(first.Rows) != 2 || first.Rows[0].ID != ids[0] || first.Rows[1].ID != ids[2] || first.NextAfter == nil || *first.NextAfter != ids[2] {
		t.Fatal("actual bounded signed-ID window changed", err, first.Complete, first.More)
	}
	for _, row := range first.Rows {
		v := row.Receipt.Native().(*pb.LeaseTimeToLiveResponse)
		if !row.Receipt.Completed || row.Receipt.WriteOutcomeUnknown || v.ID != row.ID || v.TTL <= 0 || v.GrantedTTL != 60 || len(v.Keys) != 0 {
			t.Fatal("actual row receipt changed")
		}
	}
	second, err := c.BrowseLeases(context.Background(), LeasePageRequest{Limit: 200, After: first.NextAfter})
	if err != nil || !second.Complete || second.More || len(second.Rows) != 1 || second.Rows[0].ID != ids[1] || second.NextAfter != nil {
		t.Fatal("exclusive next window changed", err)
	}
	prefix := nativeKey(t)
	for i := 0; i < 257; i++ {
		key := append(bytes.Clone(prefix), byte(i>>8), byte(i), 0, 255)
		if i == 256 {
			key = append(key, bytes.Repeat([]byte{255}, 4096-len(key))...)
		}
		nativeDo(t, admin, &pb.PutRequest{Key: key, Value: []byte{0, 255}, Lease: ids[0]})
	}
	r, err := c.ObserveLease(context.Background(), ids[0], true)
	if err != nil || !r.Completed || r.WriteOutcomeUnknown {
		t.Fatal("attached-key read failed", err)
	}
	v := r.Native().(*pb.LeaseTimeToLiveResponse)
	if v.ID != ids[0] || len(v.Keys) != 257 {
		t.Fatal("complete attached keys clipped", len(v.Keys))
	}
	maxKey := false
	for _, key := range v.Keys {
		if len(key) == 4096 {
			maxKey = true
		}
	}
	if !maxKey || f.listCalls.Load() != 2 || f.ttlCalls.Load() != 4 || f.writes.Load() != 0 {
		t.Fatal("read browser changed bytes or sent a write")
	}
	nativeDo(t, admin, &pb.LeaseRevokeRequest{ID: ids[0]})
	live[ids[0]] = false
	r, err = c.ObserveLease(context.Background(), ids[0], true)
	if err != nil || !r.Completed || r.Native().(*pb.LeaseTimeToLiveResponse).TTL != -1 {
		t.Fatal("absent TTL replaced", err)
	}
}

func TestEtcdNativeLeaseBrowserActualProcessedListAndTTLFailureNeverRetries(t *testing.T) {
	admin := nativeLeaseBrowserClient(t)
	for _, id := range []int64{9007199254742993, 9007199254742994} {
		nativeDo(t, admin, &pb.LeaseGrantRequest{TTL: 60, ID: id})
		t.Cleanup(func() { nativeDo(t, admin, &pb.LeaseRevokeRequest{ID: id}) })
	}
	for _, target := range []struct {
		method string
		after  int32
	}{
		{"/etcdserverpb.Lease/LeaseLeases", 1},
		{"/etcdserverpb.Lease/LeaseTimeToLive", 2},
	} {
		for _, mode := range []string{"lost", "cancel", "close"} {
			t.Run(target.method+"/"+mode, func(t *testing.T) {
				c, f := leaseBrowserProxy(t, admin, target.method, mode, target.after)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				type result struct {
					page LeasePage
					err  error
				}
				out := make(chan result, 1)
				base := int64(9007199254742992)
				go func() { p, e := c.BrowseLeases(ctx, LeasePageRequest{Limit: 2, After: &base}); out <- result{p, e} }()
				waitSignal(t, f.entered)
				if mode == "cancel" {
					cancel()
				} else if mode == "close" {
					if err := c.Close(); err != nil {
						t.Fatal(err)
					}
				}
				var got result
				select {
				case got = <-out:
				case <-time.After(8 * time.Second):
					t.Fatal("original read did not settle")
				}
				waitSignal(t, f.done)
				if got.err == nil || got.page.Complete || got.page.NextAfter != nil || c.Alive() || f.listCalls.Load() != 1 || f.writes.Load() != 0 {
					t.Fatal("incomplete actual read retried or misclassified", got.err)
				}
				if target.after == 2 {
					if !got.page.List.Completed || len(got.page.Rows) != 2 || !got.page.Rows[0].Receipt.Completed || !got.page.Rows[1].Receipt.Attempted || got.page.Rows[1].Receipt.Completed || got.page.Rows[1].Receipt.WriteOutcomeUnknown || f.ttlCalls.Load() != 2 {
						t.Fatal("partial original receipts replaced")
					}
				} else if got.page.List.Completed || !got.page.List.Attempted || len(got.page.Rows) != 0 || f.ttlCalls.Load() != 0 {
					t.Fatal("failed list projected historical IDs")
				}
				if _, err := c.ObserveLease(context.Background(), 9007199254742993, false); !errors.Is(err, ErrRetired) {
					t.Fatal("retired read silently reopened", err)
				}
			})
		}
	}
}

func TestEtcdNativeLeaseBrowserActualForeignReceiptsAndUnknownFields(t *testing.T) {
	admin := nativeLeaseBrowserClient(t)
	id := int64(9007199254743993)
	nativeDo(t, admin, &pb.LeaseGrantRequest{TTL: 60, ID: id})
	t.Cleanup(func() { nativeDo(t, admin, &pb.LeaseRevokeRequest{ID: id}) })
	for _, target := range []struct{ method, mode string }{
		{"/etcdserverpb.Lease/LeaseTimeToLive", "foreign-id"},
		{"/etcdserverpb.Lease/LeaseLeases", "duplicate-id"},
		{"/etcdserverpb.Lease/LeaseLeases", "oversized-list"},
		{"/etcdserverpb.Lease/LeaseTimeToLive", "unknown-fields"},
		{"/etcdserverpb.Lease/LeaseLeases", "unknown-fields"},
	} {
		t.Run(target.mode+target.method, func(t *testing.T) {
			c, f := leaseBrowserProxy(t, admin, target.method, target.mode, 1)
			var receipt Response
			var err error
			if target.method == "/etcdserverpb.Lease/LeaseTimeToLive" {
				receipt, err = c.ObserveLease(context.Background(), id, true)
			} else {
				base := id - 1
				page, e := c.BrowseLeases(context.Background(), LeasePageRequest{Limit: 1, After: &base})
				receipt, err = page.List, e
				if target.mode == "oversized-list" && (page.Complete || len(page.Rows) != 0 || page.NextAfter != nil || f.ttlCalls.Load() != 0) {
					t.Fatal("oversized original list projected a clipped live page")
				}
			}
			waitSignal(t, f.done)
			if target.mode == "unknown-fields" {
				if err != nil || !receipt.Completed || !bytes.Equal(receipt.Native().ProtoReflect().GetUnknown(), []byte{0xa0, 6, 7}) {
					t.Fatal("complete original unknown fields dropped", err)
				}
			} else if target.mode == "oversized-list" {
				if !errors.Is(err, ErrBounds) || !receipt.Completed || len(receipt.Native().(*pb.LeaseLeasesResponse).Leases) != 10001 || !c.Alive() {
					t.Fatal("bounded presentation changed the full original list", err)
				}
			} else if !errors.Is(err, ErrProtocol) || receipt.Completed || receipt.WriteOutcomeUnknown || receipt.Native() == nil || c.Alive() {
				t.Fatal("foreign actual reply accepted or lost original receipt", err)
			}
			if f.writes.Load() != 0 {
				t.Fatal("read generated lease write")
			}
		})
	}
}
