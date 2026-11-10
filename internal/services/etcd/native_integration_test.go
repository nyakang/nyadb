package etcd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func nativeConfig(t *testing.T) (Config, string) {
	t.Helper()
	if os.Getenv("NYADB_ETCD_NATIVE_IT") != "1" {
		t.Skip("requires owned etcd fixture")
	}
	cfg := Config{Endpoint: os.Getenv("NYADB_ETCD_ENDPOINT"), CAFile: os.Getenv("NYADB_ETCD_CA"), ClientCertFile: os.Getenv("NYADB_ETCD_CERT"), ClientKeyFile: os.Getenv("NYADB_ETCD_KEY"), ConnectTimeout: 5 * time.Second, RequestTimeout: 5 * time.Second}
	secret := ""
	if os.Getenv("NYADB_ETCD_MODE") != "anonymous" {
		cfg.Auth, cfg.Username, secret = "basic", os.Getenv("NYADB_ETCD_USERNAME"), os.Getenv("NYADB_ETCD_SECRET")
	}
	if cfg.Endpoint == "" || os.Getenv("NYADB_ETCD_VERSION") == "" {
		t.Fatal("incomplete native fixture")
	}
	return cfg, secret
}
func nativeClient(t *testing.T) *Client {
	t.Helper()
	cfg, secret := nativeConfig(t)
	c, err := Open(context.Background(), cfg, secret)
	if err != nil {
		t.Fatal("native open", err)
	}
	t.Cleanup(func() {
		if err := c.Close(); err != nil {
			t.Error("native close", err)
		}
	})
	return c
}
func nativeDo(t *testing.T, c *Client, req proto.Message) Response {
	t.Helper()
	p, err := c.Prepare(req)
	if err != nil {
		t.Fatal("native prepare", err)
	}
	r, err := c.Execute(context.Background(), p)
	if err != nil || !r.Attempted || !r.Completed || r.WriteOutcomeUnknown {
		t.Fatal("native completion", err, r.Completed, r.WriteOutcomeUnknown)
	}
	return r
}
func putOp(k, v []byte, lease int64) *pb.RequestOp {
	return &pb.RequestOp{Request: &pb.RequestOp_RequestPut{RequestPut: &pb.PutRequest{Key: k, Value: v, Lease: lease}}}
}
func delOp(k []byte) *pb.RequestOp {
	return &pb.RequestOp{Request: &pb.RequestOp_RequestDeleteRange{RequestDeleteRange: &pb.DeleteRangeRequest{Key: k}}}
}
func modCompare(k []byte, rev int64) *pb.Compare {
	return &pb.Compare{Key: k, Target: pb.Compare_MOD, Result: pb.Compare_EQUAL, TargetUnion: &pb.Compare_ModRevision{ModRevision: rev}}
}
func absentCompare(k []byte) *pb.Compare {
	return &pb.Compare{Key: k, Target: pb.Compare_CREATE, Result: pb.Compare_EQUAL, TargetUnion: &pb.Compare_CreateRevision{CreateRevision: 0}}
}
func nativeKey(t *testing.T) []byte {
	return []byte(fmt.Sprintf("/nyadb/%s/%d/", t.Name(), time.Now().UnixNano()))
}

func TestEtcdNativeAuthenticationIdentityAndTransport(t *testing.T) {
	c := nativeClient(t)
	if c.Identity().Version != os.Getenv("NYADB_ETCD_VERSION") || c.Identity().ClusterID == 0 || c.Identity().MemberID == 0 {
		t.Fatal("unexpected original identity")
	}
	cfg, secret := nativeConfig(t)
	if cfg.Auth == "basic" {
		if !strings.HasPrefix(cfg.Username, " ") {
			t.Fatal("fixture must exercise untrimmed identity")
		}
		if bad, err := Open(context.Background(), cfg, secret+"incorrect"); err == nil {
			bad.Close()
			t.Fatal("wrong native password accepted")
		}
		trimmed := cfg
		trimmed.Username = strings.TrimSpace(cfg.Username)
		if bad, err := Open(context.Background(), trimmed, secret); err == nil {
			bad.Close()
			t.Fatal("username silently trimmed")
		}
		if cfg.ClientCertFile == "" { // CN authentication is a separate native identity.
			limited := cfg
			limited.Username = "restricted"
			limited.ProbeKey = []byte("/allowed/key")
			d, err := Open(context.Background(), limited, secret)
			if err != nil {
				t.Fatal("restricted readable prefix", err)
			}
			defer d.Close()
			nativeDo(t, d, &pb.RangeRequest{Key: []byte("/allowed/key")})
			p, _ := d.Prepare(&pb.PutRequest{Key: []byte("/allowed/key"), Value: []byte("denied")})
			r, err := d.Execute(context.Background(), p)
			if status.Code(err) != codes.PermissionDenied || r.Completed || r.WriteOutcomeUnknown || !d.Alive() {
				t.Fatal("native permission denial misclassified", err)
			}
			p, _ = d.Prepare(PrefixRange(nil, 10, 0))
			_, err = d.Execute(context.Background(), p)
			if status.Code(err) != codes.PermissionDenied {
				t.Fatal("prefix permission inferred as root", err)
			}
		}
	}
	if cfg.CAFile != "" {
		wrong := cfg
		wrong.ServerName = "wrong.invalid"
		if bad, err := Open(context.Background(), wrong, secret); err == nil {
			bad.Close()
			t.Fatal("hostname mismatch accepted")
		}
		wrong = cfg
		wrong.CAFile = ""
		if bad, err := Open(context.Background(), wrong, secret); err == nil {
			bad.Close()
			t.Fatal("untrusted certificate accepted")
		}
	}
	if cfg.ClientCertFile != "" {
		wrong := cfg
		wrong.ClientCertFile, wrong.ClientKeyFile = "", ""
		if bad, err := Open(context.Background(), wrong, secret); err == nil {
			bad.Close()
			t.Fatal("mTLS missing certificate accepted")
		}
		certOnly := cfg
		certOnly.Auth, certOnly.Username = "none", ""
		d, err := Open(context.Background(), certOnly, "")
		if err != nil {
			t.Fatal("native certificate CN authentication", err)
		}
		defer d.Close()
		nativeDo(t, d, &pb.RangeRequest{Key: []byte("/certificate-auth")})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Open(ctx, cfg, secret); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled opening", err)
	}
	if cfg.Auth == "basic" && cfg.ClientCertFile == "" {
		// Disabling/enabling native authentication revokes the original token.
		// Only this owned server is affected; the next Open authenticates anew.
		if _, err := pb.NewAuthClient(c.conn).AuthDisable(c.authContext(context.Background()), &pb.AuthDisableRequest{}); err != nil {
			t.Fatal("owned token revocation", err)
		}
		if _, err := pb.NewAuthClient(c.conn).AuthEnable(context.Background(), &pb.AuthEnableRequest{}); err != nil {
			t.Fatal("owned auth re-enable", err)
		}
		key := nativeKey(t)
		p, _ := c.Prepare(&pb.PutRequest{Key: key, Value: []byte("must-not-retry-with-new-token")})
		r, err := c.Execute(context.Background(), p)
		if status.Code(err) != codes.Unauthenticated || r.Completed || r.WriteOutcomeUnknown || c.Alive() {
			t.Fatal("revoked native token auto-refreshed or misclassified", err)
		}
		recovered := nativeClient(t)
		if len(nativeDo(t, recovered, &pb.RangeRequest{Key: key}).Native().(*pb.RangeResponse).Kvs) != 0 {
			t.Fatal("revoked original write replayed on recovery")
		}
	}
}

func TestEtcdNativeBinaryMVCCPagingAndCompaction(t *testing.T) {
	c := nativeClient(t)
	prefix := append(nativeKey(t), 0xff, 0)
	keys := [][]byte{append(bytes.Clone(prefix), 0), append(bytes.Clone(prefix), 0, 0), append(bytes.Clone(prefix), 0xfe), append(bytes.Clone(prefix), 0xff)}
	for i, k := range keys {
		nativeDo(t, c, &pb.PutRequest{Key: k, Value: []byte{0, 0xff, byte(i)}})
	}
	first := nativeDo(t, c, PrefixRange(prefix, 2, 0)).Native().(*pb.RangeResponse)
	if len(first.Kvs) != 2 || !first.More || first.Count != 4 || !bytes.Equal(first.Kvs[0].Key, keys[0]) || !bytes.Equal(first.Kvs[0].Value, []byte{0, 255, 0}) {
		t.Fatal("binary prefix page")
	}
	rev := first.Header.Revision
	nativeDo(t, c, &pb.PutRequest{Key: keys[2], Value: []byte("later")})
	next := PrefixRange(prefix, 2, rev)
	next.Key = AfterKey(first.Kvs[1].Key)
	second := nativeDo(t, c, next).Native().(*pb.RangeResponse)
	if len(second.Kvs) != 2 || second.More || !bytes.Equal(second.Kvs[0].Value, []byte{0, 255, 2}) {
		t.Fatal("MVCC page changed")
	}
	for i, kv := range append(first.Kvs, second.Kvs...) {
		if !bytes.Equal(kv.Key, keys[i]) || kv.Version != 1 || kv.CreateRevision != kv.ModRevision {
			t.Fatal("native row metadata")
		}
	}
	count := PrefixRange(prefix, 0, rev)
	count.CountOnly = true
	if nativeDo(t, c, count).Native().(*pb.RangeResponse).Count != 4 {
		t.Fatal("snapshot count")
	}
	// Compact is deliberately fixture preparation, not an exposed management API.
	_, err := pb.NewKVClient(c.conn).Compact(c.authContext(context.Background()), &pb.CompactionRequest{Revision: rev})
	if err != nil {
		t.Fatal("native compaction fixture", err)
	}
	p, _ := c.Prepare(&pb.RangeRequest{Key: keys[0], Revision: rev - 1})
	r, err := c.Execute(context.Background(), p)
	if status.Code(err) != codes.OutOfRange || r.Completed || !c.Alive() {
		t.Fatal("compacted revision was silently moved", err)
	}
	raw := first.Kvs[0].Value
	raw[0] = 100
	fresh := nativeDo(t, c, &pb.RangeRequest{Key: keys[0]}).Native().(*pb.RangeResponse)
	if fresh.Kvs[0].Value[0] != 0 {
		t.Fatal("retained bytes alias native server")
	}
	// A bounded row count alone does not bound large values. The native channel
	// rejects an oversized complete response instead of accepting partial data.
	bounded := nativeClient(t)
	largePrefix := nativeKey(t)
	for i := 0; i < 17; i++ {
		nativeDo(t, bounded, &pb.PutRequest{Key: append(bytes.Clone(largePrefix), byte(i)), Value: bytes.Repeat([]byte{byte(i)}, 1<<20)})
	}
	count = PrefixRange(largePrefix, 0, 0)
	count.CountOnly = true
	if nativeDo(t, bounded, count).Native().(*pb.RangeResponse).Count != 17 {
		t.Fatal("large value fixture count")
	}
	p, _ = bounded.Prepare(PrefixRange(largePrefix, 17, 0))
	r, err = bounded.Execute(context.Background(), p)
	if status.Code(err) != codes.ResourceExhausted || r.Completed || r.WriteOutcomeUnknown || bounded.Alive() {
		t.Fatal("oversized native response accepted or misclassified", err)
	}
}

func TestEtcdNativeAtomicComparisonsRenameAndConflicts(t *testing.T) {
	c := nativeClient(t)
	key := nativeKey(t)
	target := append(bytes.Clone(key), 'b')
	initial := nativeDo(t, c, &pb.TxnRequest{Compare: []*pb.Compare{absentCompare(key)}, Success: []*pb.RequestOp{putOp(key, []byte{0, 255}, 0)}}).Native().(*pb.TxnResponse)
	if !initial.Succeeded || len(initial.Responses) != 1 {
		t.Fatal("native create comparison")
	}
	old := nativeDo(t, c, &pb.RangeRequest{Key: key}).Native().(*pb.RangeResponse).Kvs[0]
	compares := []*pb.Compare{modCompare(key, old.ModRevision), {Key: key, Target: pb.Compare_CREATE, TargetUnion: &pb.Compare_CreateRevision{CreateRevision: old.CreateRevision}}, {Key: key, Target: pb.Compare_VERSION, TargetUnion: &pb.Compare_Version{Version: 1}}, {Key: key, Target: pb.Compare_VALUE, TargetUnion: &pb.Compare_Value{Value: bytes.Clone(old.Value)}}}
	updated := nativeDo(t, c, &pb.TxnRequest{Compare: compares, Success: []*pb.RequestOp{putOp(key, []byte("new"), 0), putOp(target, []byte("other"), 0)}}).Native().(*pb.TxnResponse)
	if !updated.Succeeded || len(updated.Responses) != 2 {
		t.Fatal("atomic compare update")
	}
	a := nativeDo(t, c, &pb.RangeRequest{Key: key}).Native().(*pb.RangeResponse).Kvs[0]
	b := nativeDo(t, c, &pb.RangeRequest{Key: target}).Native().(*pb.RangeResponse).Kvs[0]
	if a.ModRevision != b.ModRevision || a.Version != 2 || a.CreateRevision != old.CreateRevision {
		t.Fatal("one revision native atomic batch")
	}
	conflict := nativeDo(t, c, &pb.TxnRequest{Compare: []*pb.Compare{modCompare(key, old.ModRevision)}, Success: []*pb.RequestOp{putOp(key, []byte("bad"), 0), delOp(target)}}).Native().(*pb.TxnResponse)
	if conflict.Succeeded || len(conflict.Responses) != 0 {
		t.Fatal("changed observation not a conflict")
	}
	if !bytes.Equal(nativeDo(t, c, &pb.RangeRequest{Key: key}).Native().(*pb.RangeResponse).Kvs[0].Value, []byte("new")) {
		t.Fatal("conflicting batch wrote")
	}
	newKey := append(bytes.Clone(key), 'c')
	renamed := nativeDo(t, c, &pb.TxnRequest{Compare: []*pb.Compare{modCompare(key, a.ModRevision), absentCompare(newKey)}, Success: []*pb.RequestOp{putOp(newKey, a.Value, a.Lease), delOp(key)}}).Native().(*pb.TxnResponse)
	if !renamed.Succeeded || len(nativeDo(t, c, &pb.RangeRequest{Key: key}).Native().(*pb.RangeResponse).Kvs) != 0 {
		t.Fatal("native atomic rename")
	}
	if nativeDo(t, c, &pb.RangeRequest{Key: newKey}).Native().(*pb.RangeResponse).Kvs[0].CreateRevision == a.CreateRevision {
		t.Fatal("rename falsely preserved key creation identity")
	}
	dup := &pb.TxnRequest{Success: []*pb.RequestOp{putOp(newKey, []byte("dup1"), 0), putOp(newKey, []byte("dup2"), 0)}}
	p, _ := c.Prepare(dup)
	r, err := c.Execute(context.Background(), p)
	if status.Code(err) != codes.InvalidArgument || r.WriteOutcomeUnknown || r.Completed {
		t.Fatal("duplicate native write acceptance", err)
	}
	if !bytes.Equal(nativeDo(t, c, &pb.RangeRequest{Key: newKey}).Native().(*pb.RangeResponse).Kvs[0].Value, []byte("new")) {
		t.Fatal("native rejected batch partially wrote")
	}
}

func TestEtcdNativeLeaseAttachmentKeepAliveAndExpiration(t *testing.T) {
	c := nativeClient(t)
	key := nativeKey(t)
	lease := nativeDo(t, c, &pb.LeaseGrantRequest{TTL: 3, ID: 9007199254740993}).Native().(*pb.LeaseGrantResponse)
	if lease.ID != 9007199254740993 {
		t.Fatal("native lease ID lost integer precision")
	}
	nativeDo(t, c, &pb.PutRequest{Key: key, Value: []byte("attached"), Lease: lease.ID})
	nativeDo(t, c, &pb.PutRequest{Key: key, Value: []byte("preserved"), IgnoreLease: true})
	read := nativeDo(t, c, &pb.RangeRequest{Key: key}).Native().(*pb.RangeResponse).Kvs[0]
	if read.Lease != lease.ID {
		t.Fatal("native IgnoreLease detached lease")
	}
	cmp := &pb.Compare{Key: key, Target: pb.Compare_LEASE, TargetUnion: &pb.Compare_Lease{Lease: lease.ID}}
	if !nativeDo(t, c, &pb.TxnRequest{Compare: []*pb.Compare{cmp}, Success: []*pb.RequestOp{putOp(append(bytes.Clone(key), 'b'), []byte("second"), lease.ID)}}).Native().(*pb.TxnResponse).Succeeded {
		t.Fatal("native lease comparison")
	}
	ttl := nativeDo(t, c, &pb.LeaseTimeToLiveRequest{ID: lease.ID, Keys: true}).Native().(*pb.LeaseTimeToLiveResponse)
	if ttl.ID != lease.ID || ttl.TTL < 1 || ttl.GrantedTTL < 3 || len(ttl.Keys) != 2 {
		t.Fatal("native lease keys/TTL")
	}
	keep := nativeDo(t, c, &pb.LeaseKeepAliveRequest{ID: lease.ID}).Native().(*pb.LeaseKeepAliveResponse)
	if keep.ID != lease.ID || keep.TTL < 1 {
		t.Fatal("one native keepalive")
	}
	nativeDo(t, c, &pb.LeaseRevokeRequest{ID: lease.ID})
	if len(nativeDo(t, c, &pb.RangeRequest{Key: key}).Native().(*pb.RangeResponse).Kvs) != 0 {
		t.Fatal("revoke did not delete attached key")
	}
	missing := nativeDo(t, c, &pb.LeaseTimeToLiveRequest{ID: lease.ID}).Native().(*pb.LeaseTimeToLiveResponse)
	if missing.TTL != -1 {
		t.Fatal("missing lease sentinel lost")
	}
	lease = nativeDo(t, c, &pb.LeaseGrantRequest{TTL: 1}).Native().(*pb.LeaseGrantResponse)
	nativeDo(t, c, &pb.PutRequest{Key: key, Value: []byte("expires"), Lease: lease.ID})
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if len(nativeDo(t, c, &pb.RangeRequest{Key: key}).Native().(*pb.RangeResponse).Kvs) == 0 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("client unexpectedly maintained lease automatically")
}

// The proxy forwards to the real server, observes a native success, and then
// loses/holds that exact reply. It is not a substitute implementation of etcd.
type nativeFault struct {
	upstream      *Client
	method, mode  string
	calls         atomic.Int32
	entered, done chan struct{}
	seen          atomic.Bool
	ready         atomic.Bool
	receipt       proto.Message // published by entered/done channels
}

func (f *nativeFault) serve(_ any, stream grpc.ServerStream) error {
	method, _ := grpc.MethodFromServerStream(stream)
	var req, res proto.Message
	switch method {
	case "/etcdserverpb.Auth/AuthStatus":
		req, res = &pb.AuthStatusRequest{}, &pb.AuthStatusResponse{}
	case "/etcdserverpb.Auth/UserGet":
		req, res = &pb.AuthUserGetRequest{}, &pb.AuthUserGetResponse{}
	case "/etcdserverpb.Auth/RoleGet":
		req, res = &pb.AuthRoleGetRequest{}, &pb.AuthRoleGetResponse{}
	case "/etcdserverpb.Auth/Authenticate":
		req, res = &pb.AuthenticateRequest{}, &pb.AuthenticateResponse{}
	case "/etcdserverpb.Maintenance/Status":
		req, res = &pb.StatusRequest{}, &pb.StatusResponse{}
	case "/etcdserverpb.KV/Range":
		req, res = &pb.RangeRequest{}, &pb.RangeResponse{}
	case "/etcdserverpb.KV/Put":
		req, res = &pb.PutRequest{}, &pb.PutResponse{}
	case "/etcdserverpb.KV/Txn":
		req, res = &pb.TxnRequest{}, &pb.TxnResponse{}
	case "/etcdserverpb.Lease/LeaseGrant":
		req, res = &pb.LeaseGrantRequest{}, &pb.LeaseGrantResponse{}
	case "/etcdserverpb.Lease/LeaseRevoke":
		req, res = &pb.LeaseRevokeRequest{}, &pb.LeaseRevokeResponse{}
	case "/etcdserverpb.Lease/LeaseKeepAlive":
		req, res = &pb.LeaseKeepAliveRequest{}, &pb.LeaseKeepAliveResponse{}
	default:
		return status.Error(codes.Unimplemented, "not a fixture route")
	}
	if err := stream.RecvMsg(req); err != nil {
		return err
	}
	var call int32
	if f.ready.Load() && method == f.method {
		call = f.calls.Add(1)
	}
	target := call > 0 && (f.mode != "read-block-second" || call == 2) && f.seen.CompareAndSwap(false, true)
	if target && (f.mode == "read-block" || f.mode == "read-block-second") {
		close(f.entered)
		<-stream.Context().Done()
		close(f.done)
		return stream.Context().Err()
	}
	ctx := f.upstream.authContext(stream.Context())
	var err error
	if method == "/etcdserverpb.Lease/LeaseKeepAlive" {
		var upstream pb.Lease_LeaseKeepAliveClient
		upstream, err = pb.NewLeaseClient(f.upstream.conn).LeaseKeepAlive(ctx)
		if err == nil {
			err = upstream.Send(req.(*pb.LeaseKeepAliveRequest))
		}
		if err == nil {
			err = upstream.CloseSend()
		}
		if err == nil {
			res, err = upstream.Recv()
		}
	} else {
		err = f.upstream.conn.Invoke(ctx, method, req, res)
	}
	if err != nil {
		return err
	}
	if target {
		f.receipt = proto.Clone(res)
		// A later independent update must survive any lost original reply.
		if put, ok := req.(*pb.PutRequest); ok {
			_, err = pb.NewKVClient(f.upstream.conn).Put(ctx, &pb.PutRequest{Key: put.Key, Value: []byte("later-independent")})
		} else if txn, ok := req.(*pb.TxnRequest); ok {
			for _, op := range txn.Success {
				if put := op.GetRequestPut(); put != nil {
					_, err = pb.NewKVClient(f.upstream.conn).Put(ctx, &pb.PutRequest{Key: put.Key, Value: []byte("later-independent")})
					break
				}
			}
		}
		if err != nil {
			return err
		}
		close(f.entered)
		if f.mode == "incomplete" {
			res.(*pb.TxnResponse).Responses = nil
			close(f.done)
			return stream.SendMsg(res)
		}
		if f.mode != "lost" {
			<-stream.Context().Done()
		}
		close(f.done)
		return status.Error(codes.Unavailable, "actual native reply discarded by owned fixture")
	}
	return stream.SendMsg(res)
}
func faultClient(t *testing.T, upstream *Client, method, mode string) (*Client, *nativeFault) {
	cfg, secret := nativeConfig(t)
	return faultClientConfig(t, upstream, cfg, secret, method, mode)
}
func faultClientConfig(t *testing.T, upstream *Client, cfg Config, secret, method, mode string) (*Client, *nativeFault) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &nativeFault{upstream: upstream, method: method, mode: mode, entered: make(chan struct{}), done: make(chan struct{})}
	server := grpc.NewServer(grpc.UnknownServiceHandler(f.serve))
	go server.Serve(listener)
	t.Cleanup(func() { server.Stop(); listener.Close() })
	cfg.Endpoint = "http://" + listener.Addr().String()
	cfg.CAFile, cfg.ClientCertFile, cfg.ClientKeyFile, cfg.ServerName = "", "", "", ""
	c, err := Open(context.Background(), cfg, secret)
	if err != nil {
		t.Fatal("original fault client open", err)
	}
	t.Cleanup(func() { c.Close() })
	f.ready.Store(true)
	return c, f
}
func waitSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(8 * time.Second):
		t.Fatal("original fixture executor did not acknowledge")
	}
}

func TestEtcdNativeActualCommittedReplyLossCancellationAndOriginalClose(t *testing.T) {
	upstream := nativeClient(t)
	for _, kind := range []string{"put", "txn", "grant", "keepalive", "revoke"} {
		for _, mode := range []string{"lost", "cancel", "close"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				key := nativeKey(t)
				var req proto.Message
				var originalLease int64
				beforeLeases := nativeDo(t, upstream, &pb.LeaseLeasesRequest{}).Native().(*pb.LeaseLeasesResponse)
				switch kind {
				case "put":
					req = &pb.PutRequest{Key: key, Value: []byte("original")}
				case "txn":
					req = &pb.TxnRequest{Success: []*pb.RequestOp{putOp(key, []byte("original"), 0)}}
				case "grant":
					req = &pb.LeaseGrantRequest{TTL: 30}
				case "keepalive", "revoke":
					lease := nativeDo(t, upstream, &pb.LeaseGrantRequest{TTL: 30}).Native().(*pb.LeaseGrantResponse)
					originalLease = lease.ID
					if kind == "revoke" {
						nativeDo(t, upstream, &pb.PutRequest{Key: key, Value: []byte("attached-original"), Lease: lease.ID})
					}
					if kind == "keepalive" {
						req = &pb.LeaseKeepAliveRequest{ID: lease.ID}
					} else {
						req = &pb.LeaseRevokeRequest{ID: lease.ID}
					}
				}
				method, _, _ := validateRequest(req)
				c, f := faultClient(t, upstream, method, mode)
				p, err := c.Prepare(req)
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				type outcome struct {
					r   Response
					err error
				}
				out := make(chan outcome, 1)
				go func() { r, e := c.Execute(ctx, p); out <- outcome{r, e} }()
				waitSignal(t, f.entered)
				if mode == "cancel" {
					cancel()
				} else if mode == "close" {
					if err := c.Close(); err != nil {
						t.Fatal(err)
					}
				}
				var result outcome
				select {
				case result = <-out:
				case <-time.After(8 * time.Second):
					t.Fatal("original request did not settle")
				}
				waitSignal(t, f.done)
				if f.receipt == nil || !upstream.matches(responseHeader(f.receipt)) {
					t.Fatal("fault did not discard an actual successful native receipt")
				}
				var interrupted *Interrupted
				if !errors.As(result.err, &interrupted) || !result.r.Attempted || result.r.Completed || !result.r.WriteOutcomeUnknown || c.Alive() || f.calls.Load() != 1 {
					t.Fatal("processed native write replayed or misclassified", result.err, f.calls.Load())
				}
				if _, err := c.Execute(context.Background(), p); !errors.Is(err, ErrRetired) {
					t.Fatal("lost original request allowed reuse", err)
				}
				if kind == "put" || kind == "txn" {
					observed := nativeDo(t, upstream, &pb.RangeRequest{Key: key}).Native().(*pb.RangeResponse)
					if len(observed.Kvs) != 1 || !bytes.Equal(observed.Kvs[0].Value, []byte("later-independent")) || observed.Kvs[0].Version != 2 {
						t.Fatal("later independent value overwritten")
					}
				}
				if kind == "grant" {
					grant := f.receipt.(*pb.LeaseGrantResponse)
					if grant.ID == 0 || grant.TTL < 1 || grant.Error != "" {
						t.Fatal("fault had no granted original lease")
					}
					listed := nativeDo(t, upstream, &pb.LeaseLeasesRequest{}).Native().(*pb.LeaseLeasesResponse)
					if len(listed.Leases) != len(beforeLeases.Leases)+1 {
						t.Fatal("lost grant allocated additional leases")
					}
					nativeDo(t, upstream, &pb.LeaseRevokeRequest{ID: grant.ID})
				} else if kind == "keepalive" {
					keep := f.receipt.(*pb.LeaseKeepAliveResponse)
					if keep.ID != originalLease || keep.TTL < 1 {
						t.Fatal("fault had no original lease keepalive")
					}
					nativeDo(t, upstream, &pb.LeaseRevokeRequest{ID: originalLease})
				} else if kind == "revoke" {
					if len(nativeDo(t, upstream, &pb.RangeRequest{Key: key}).Native().(*pb.RangeResponse).Kvs) != 0 {
						t.Fatal("lost revoke did not delete actual attached key")
					}
				}
				if err := c.Close(); err != nil {
					t.Fatal("idempotent retired close", err)
				}
				if !upstream.Alive() {
					t.Fatal("original close retired a replacement")
				}
			})
		}
	}
}

func TestEtcdNativeOriginalReadCancellationBusyAndPreparationOwnership(t *testing.T) {
	upstream := nativeClient(t)
	key := nativeKey(t)
	nativeDo(t, upstream, &pb.PutRequest{Key: key, Value: []byte("retained")})
	for _, mode := range []string{"cancel", "close"} {
		t.Run(mode, func(t *testing.T) {
			c, f := faultClient(t, upstream, "/etcdserverpb.KV/Range", "read-block")
			p, _ := c.Prepare(&pb.RangeRequest{Key: key})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type outcome struct {
				r   Response
				err error
			}
			out := make(chan outcome, 1)
			go func() { r, e := c.Execute(ctx, p); out <- outcome{r, e} }()
			waitSignal(t, f.entered)
			queued, _ := c.Prepare(&pb.PutRequest{Key: key, Value: []byte("must-not-dispatch")})
			if _, err := c.Execute(context.Background(), queued); !errors.Is(err, ErrBusy) {
				t.Fatal("busy original admitted another request", err)
			}
			if mode == "cancel" {
				cancel()
			} else {
				if err := c.Close(); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case result := <-out:
				if !errors.Is(result.err, ErrRetired) || result.r.Completed || result.r.WriteOutcomeUnknown {
					t.Fatal("read cancellation misclassified", result.err)
				}
			case <-time.After(8 * time.Second):
				t.Fatal("original read did not settle")
			}
			waitSignal(t, f.done)
			if _, err := c.Execute(context.Background(), queued); !errors.Is(err, ErrRetired) {
				t.Fatal("queued request reused retired client", err)
			}
			if f.calls.Load() != 1 {
				t.Fatal("original read replayed")
			}
			if !bytes.Equal(nativeDo(t, upstream, &pb.RangeRequest{Key: key}).Native().(*pb.RangeResponse).Kvs[0].Value, []byte("retained")) {
				t.Fatal("queued write changed data")
			}
		})
	}
	// A complete success consumes a Plan even if another copy is retained.
	req := &pb.PutRequest{Key: key, Value: []byte("captured")}
	p, err := upstream.Prepare(req)
	if err != nil {
		t.Fatal(err)
	}
	copyPlan := p
	req.Value[0] = 'X'
	p.Native().(*pb.PutRequest).Value[0] = 'Y'
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := upstream.Execute(ctx, p); !errors.Is(err, context.Canceled) {
		t.Fatal("pre-dispatch cancellation", err)
	}
	ctx = metadata.NewOutgoingContext(context.Background(), metadata.Pairs("token", "foreign-invalid"))
	r, err := upstream.Execute(ctx, p)
	if err != nil {
		t.Fatal("caller token replaced original credential", err)
	}
	if _, err := upstream.Execute(context.Background(), copyPlan); !errors.Is(err, ErrUsed) {
		t.Fatal("successful original request replayed", err)
	}
	r.Native().(*pb.PutResponse).Header.Revision = 0
	if upstream.LastResponse().Header.Revision == 0 {
		t.Fatal("native result aliases retained receipt")
	}
	observed := nativeDo(t, upstream, &pb.RangeRequest{Key: key}).Native().(*pb.RangeResponse)
	if !bytes.Equal(observed.Kvs[0].Value, []byte("captured")) {
		t.Fatal("request capture mutated")
	}
	other := nativeClient(t)
	foreign, _ := upstream.Prepare(&pb.RangeRequest{Key: key})
	if _, err := other.Execute(context.Background(), foreign); !errors.Is(err, ErrUsed) {
		t.Fatal("foreign preparation accepted", err)
	}
	if !nativeDo(t, upstream, &pb.RangeRequest{Key: key}).Completed {
		t.Fatal("foreign request retired original")
	}
}
