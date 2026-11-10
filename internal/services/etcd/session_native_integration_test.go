package etcd

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"net/url"
	"os"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func sessionProfile(t *testing.T) (connections.Profile, string) {
	t.Helper()
	if os.Getenv("NYADB_ETCD_SESSION_IT") != "1" {
		t.Skip("requires owned native etcd session fixture")
	}
	cfg, secret := nativeConfig(t)
	u, _ := url.Parse(cfg.Endpoint)
	p, _ := connections.Defaults(connections.Etcd)
	p.ID, p.Name = 81, "native KV session"
	p.Host = u.Hostname()
	port, _ := strconv.Atoi(u.Port())
	p.Port = uint16(port)
	if u.Scheme == "https" {
		p.TLSMode = "verify-full"
	}
	o := &p.EtcdOptions
	o.CAFile, o.ServerName, o.ClientCertFile, o.ClientKeyFile = cfg.CAFile, cfg.ServerName, cfg.ClientCertFile, cfg.ClientKeyFile
	o.ConnectTimeoutSeconds, o.RequestTimeoutSeconds = 5, 5
	if cfg.Auth == "basic" {
		o.Auth, p.Username = "basic", cfg.Username
	}
	return p, secret
}
func nativeSession(t *testing.T) *Session {
	t.Helper()
	p, secret := sessionProfile(t)
	raw, err := OpenSession(context.Background(), p, secret)
	if err != nil {
		t.Fatalf("native session open failed (%T)", err)
	}
	s := raw.(*Session)
	t.Cleanup(func() { _ = s.Close() })
	return s
}
func nativeKVRequest(t *testing.T, m proto.Message) contract.KVRequest {
	t.Helper()
	method, _, err := validateRequest(m)
	if err != nil {
		t.Fatal(err)
	}
	b, err := (protojson.MarshalOptions{UseProtoNames: true}).Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return contract.KVRequest{Method: method, JSON: string(b)}
}
func nativeKVDo(t *testing.T, l contract.KVRequestLease, m proto.Message) contract.KVResult {
	t.Helper()
	p, err := l.Prepare(nativeKVRequest(t, m))
	if err != nil {
		t.Fatal(err)
	}
	r, err := l.Execute(context.Background(), p)
	if err != nil || !r.Attempted || !r.Completed || r.WriteOutcomeUnknown || r.Code != "OK" || r.JSON == "" || r.ProjectionError != "" || r.JSONCaptureError != "" {
		t.Fatalf("native lease execution failed (%T)", err)
	}
	return r
}
func reserveNativeKV(t *testing.T, s *Session, id uint64) contract.KVRequestLease {
	t.Helper()
	l, err := s.ReserveKV(id)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l
}
func TestEtcdNativeSessionOriginalScopePurePlansAndExactReplies(t *testing.T) {
	p, secret := sessionProfile(t)
	p.EtcdOptions.PrefixEncoding, p.EtcdOptions.Prefix = "base64", base64.StdEncoding.EncodeToString([]byte{0, 255, 'x'})
	raw, err := OpenSession(context.Background(), p, secret)
	if err != nil {
		t.Fatal(err)
	}
	s := raw.(*Session)
	defer s.Close()
	scope := s.KVScope()
	if scope.Namespace.ProfileID != p.ID || scope.Namespace.Catalog != "kv" || scope.Namespace.Schema != "" || !bytes.Equal(scope.Prefix, []byte{0, 255, 'x'}) || scope.Version != os.Getenv("NYADB_ETCD_VERSION") {
		t.Fatal("original scope changed")
	}
	scope.Prefix[0] = 1
	if s.KVScope().Prefix[0] != 0 {
		t.Fatal("scope alias")
	}
	l := reserveNativeKV(t, s, 17)
	if _, err = s.ReserveKV(17); err == nil {
		t.Fatal("duplicate document identity accepted")
	}
	var opens atomic.Int32
	s.opener = func(ctx context.Context, p connections.Profile, secret string) (*Client, error) {
		opens.Add(1)
		return OpenProfile(ctx, p, secret)
	}
	key := append(nativeKey(t), 0, 255)
	request := nativeKVRequest(t, &pb.PutRequest{Key: key, Value: []byte{0, 255, 1, 2}})
	prepared, err := l.Prepare(request)
	if err != nil || opens.Load() != 0 || !prepared.Writes() {
		t.Fatal("pure preparation opened a channel")
	}
	copy := prepared.Snapshot()
	copy.JSON = `{}`
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if r, err := l.Execute(ctx, prepared); !errors.Is(err, context.Canceled) || r.Attempted || opens.Load() != 0 {
		t.Fatal("predispatch cancellation consumed request")
	}
	if err = l.Preconnect(context.Background()); err != nil || opens.Load() != 1 {
		t.Fatal("explicit original preconnect", err)
	}
	r, err := l.Execute(context.Background(), prepared)
	if err != nil || !r.Completed {
		t.Fatal(err)
	}
	if _, err = l.Execute(context.Background(), prepared); !errors.Is(err, contract.ErrKVPlanUsed) {
		t.Fatal("one-use preparation replayed")
	}
	read := nativeKVDo(t, l, &pb.RangeRequest{Key: key})
	entry := read.Reply.Entries[0]
	if !bytes.Equal(entry.Key, key) || !bytes.Equal(entry.Value, []byte{0, 255, 1, 2}) || entry.CreateRevision <= 0 || entry.ModRevision != entry.CreateRevision || entry.Version != 1 {
		t.Fatal("absolute binary key/value or revisions changed")
	}
	clone := read.Clone()
	clone.Reply.Entries[0].Value[0] = 1
	clone.Native[0] ^= 1
	if l.LastResult().Reply.Entries[0].Value[0] != 0 || bytes.Equal(clone.Native, read.Native) {
		t.Fatal("retained receipt alias")
	}
	actual := &pb.RangeResponse{}
	if proto.Unmarshal(read.Native, actual) != nil || !bytes.Equal(actual.Kvs[0].Key, key) {
		t.Fatal("full native response lost")
	}
	grant := nativeKVDo(t, l, &pb.LeaseGrantRequest{TTL: 30, ID: 9007199254740993})
	if grant.Reply.Lease.ID != 9007199254740993 {
		t.Fatal("lease integer narrowed")
	}
	nativeKVDo(t, l, &pb.PutRequest{Key: key, Value: []byte("leased"), Lease: grant.Reply.Lease.ID, PrevKv: true})
	ttl := nativeKVDo(t, l, &pb.LeaseTimeToLiveRequest{ID: grant.Reply.Lease.ID, Keys: true})
	if ttl.Reply.Lease.ID != grant.Reply.Lease.ID || len(ttl.Reply.Lease.Keys) != 1 || !bytes.Equal(ttl.Reply.Lease.Keys[0], key) {
		t.Fatal("lease keys changed")
	}
	txn := nativeKVDo(t, l, &pb.TxnRequest{Compare: []*pb.Compare{absentCompare(key)}, Success: []*pb.RequestOp{putOp(key, []byte("must not write"), 0)}, Failure: []*pb.RequestOp{{Request: &pb.RequestOp_RequestRange{RequestRange: &pb.RangeRequest{Key: key}}}}})
	if txn.Reply.Succeeded || txn.Reply.Operations[0].Entries[0].Lease != 9007199254740993 {
		t.Fatal("definite comparison false became a failure or write")
	}
	nativeKVDo(t, l, &pb.LeaseKeepAliveRequest{ID: grant.Reply.Lease.ID})
	nativeKVDo(t, l, &pb.LeaseLeasesRequest{})
	nativeKVDo(t, l, &pb.StatusRequest{})
	nativeKVDo(t, l, &pb.LeaseRevokeRequest{ID: grant.Reply.Lease.ID})
	missing := nativeKVDo(t, l, &pb.LeaseTimeToLiveRequest{ID: grant.Reply.Lease.ID})
	if missing.Reply.Lease.TTL != -1 {
		t.Fatal("absent native lease sentinel changed")
	}
	nativeKVDo(t, l, &pb.PutRequest{Key: key, Value: []byte("delete")})
	deleted := nativeKVDo(t, l, &pb.DeleteRangeRequest{Key: key, PrevKv: true})
	if deleted.Reply.Deleted != 1 || !bytes.Equal(deleted.Reply.Entries[0].Value, []byte("delete")) {
		t.Fatal("delete receipt lost")
	}
	if descriptor, ok := connections.Describe(connections.Etcd); !ok || !descriptor.Capabilities.KeyValues || descriptor.Capabilities.Query {
		t.Fatal("native KV descriptor capability boundary changed")
	}
}
func TestEtcdNativeSessionMVCCAndNativePermissionBoundary(t *testing.T) {
	s := nativeSession(t)
	l := reserveNativeKV(t, s, 1)
	prefix := nativeKey(t)
	for i := 0; i < 4; i++ {
		nativeKVDo(t, l, &pb.PutRequest{Key: append(bytes.Clone(prefix), byte(i)), Value: []byte{byte(i)}})
	}
	first := nativeKVDo(t, l, PrefixRange(prefix, 2, 0))
	revision := first.Header.Revision
	nativeKVDo(t, l, &pb.PutRequest{Key: append(bytes.Clone(prefix), 2), Value: []byte("later")})
	nativeKVDo(t, l, &pb.PutRequest{Key: append(bytes.Clone(prefix), 4), Value: []byte("new")})
	rangeRequest := PrefixRange(prefix, 2, revision)
	rangeRequest.Key = AfterKey(first.Reply.Entries[1].Key)
	next := nativeKVDo(t, l, rangeRequest)
	if len(next.Reply.Entries) != 2 || !bytes.Equal(next.Reply.Entries[0].Value, []byte{2}) || next.Reply.More {
		t.Fatal("fixed MVCC read changed revision")
	}
	admin := nativeClient(t)
	if _, err := pb.NewKVClient(admin.conn).Compact(admin.authContext(context.Background()), &pb.CompactionRequest{Revision: next.Header.Revision}); err != nil {
		t.Fatal(err)
	}
	plan, _ := l.Prepare(nativeKVRequest(t, rangeRequest))
	r, err := l.Execute(context.Background(), plan)
	if err == nil || r.Code != "OutOfRange" || r.Completed || r.WriteOutcomeUnknown || l.CheckKVLease() != nil {
		t.Fatal("compaction silently fell back or retired a definite rejection")
	}
	fresh := nativeKVDo(t, l, PrefixRange(prefix, 10, 0))
	if len(fresh.Reply.Entries) != 5 || !bytes.Equal(fresh.Reply.Entries[2].Value, []byte("later")) {
		t.Fatal("explicit current read failed")
	}
	p, secret := sessionProfile(t)
	if p.EtcdOptions.Auth == "basic" && p.EtcdOptions.ClientCertFile == "" {
		p.Username, p.EtcdOptions.Prefix = "restricted", "/allowed/"
		raw, err := OpenSession(context.Background(), p, secret)
		if err != nil {
			t.Fatal(err)
		}
		limited := raw.(*Session)
		defer limited.Close()
		reader := reserveNativeKV(t, limited, 1)
		nativeKVDo(t, reader, &pb.RangeRequest{Key: []byte("/allowed/key")})
		for _, m := range []proto.Message{PrefixRange(nil, 2, 0), &pb.PutRequest{Key: []byte("/allowed/key"), Value: []byte("denied")}} {
			prepared, _ := reader.Prepare(nativeKVRequest(t, m))
			r, err := reader.Execute(context.Background(), prepared)
			if err == nil || r.Code != "PermissionDenied" || r.Completed || r.WriteOutcomeUnknown || reader.CheckKVLease() != nil {
				t.Fatal("successful probe inferred root or write permission")
			}
		}
	} else if p.EtcdOptions.ClientCertFile != "" {
		p.EtcdOptions.Auth, p.Username = "certificate", ""
		raw, err := OpenSession(context.Background(), p, "")
		if err != nil {
			t.Fatal(err)
		}
		defer raw.Close()
		nativeKVDo(t, reserveNativeKV(t, raw.(*Session), 1), &pb.RangeRequest{Key: []byte("/certificate-native")})
	}
}
func TestEtcdNativeSessionActualProcessedReplyFaultsAndRecovery(t *testing.T) {
	p, secret := sessionProfile(t)
	upstream := nativeClient(t)
	for _, kind := range []string{"put", "txn", "grant", "keepalive", "revoke"} {
		for _, mode := range []string{"lost", "cancel", "close"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				key := nativeKey(t)
				var req proto.Message
				var leaseID int64
				before := nativeDo(t, upstream, &pb.LeaseLeasesRequest{}).Native().(*pb.LeaseLeasesResponse)
				switch kind {
				case "put":
					req = &pb.PutRequest{Key: key, Value: []byte("original")}
				case "txn":
					req = &pb.TxnRequest{Success: []*pb.RequestOp{putOp(key, []byte("original"), 0)}}
				case "grant":
					req = &pb.LeaseGrantRequest{TTL: 120}
				case "keepalive", "revoke":
					leaseID = nativeDo(t, upstream, &pb.LeaseGrantRequest{TTL: 120}).Native().(*pb.LeaseGrantResponse).ID
					if kind == "keepalive" {
						req = &pb.LeaseKeepAliveRequest{ID: leaseID}
					} else {
						nativeDo(t, upstream, &pb.PutRequest{Key: key, Value: []byte("attached"), Lease: leaseID})
						req = &pb.LeaseRevokeRequest{ID: leaseID}
					}
				}
				method, _, _ := validateRequest(req)
				c, f := faultClient(t, upstream, method, mode)
				s := newSession(p, secret, nativeClient(t))
				defer s.Close()
				var opened atomic.Int32
				s.opener = func(ctx context.Context, p connections.Profile, secret string) (*Client, error) {
					if opened.Add(1) == 1 {
						return c, nil
					}
					return OpenProfile(ctx, p, secret)
				}
				l := reserveNativeKV(t, s, 1)
				if err := l.Preconnect(context.Background()); err != nil {
					t.Fatal(err)
				}
				prepared, err := l.Prepare(nativeKVRequest(t, req))
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				type result struct {
					r contract.KVResult
					e error
				}
				out := make(chan result, 1)
				go func() { r, e := l.Execute(ctx, prepared); out <- result{r, e} }()
				waitSignal(t, f.entered)
				if mode == "cancel" {
					cancel()
				} else if mode == "close" {
					if err = l.Close(); err != nil {
						t.Fatal(err)
					}
				}
				var settled result
				select {
				case settled = <-out:
				case <-time.After(8 * time.Second):
					t.Fatal("original native lease executor did not settle")
				}
				waitSignal(t, f.done)
				if settled.e == nil || !settled.r.Attempted || settled.r.Completed || !settled.r.WriteOutcomeUnknown || f.calls.Load() != 1 || f.receipt == nil || !upstream.matches(responseHeader(f.receipt)) {
					t.Fatal("original processed reply replayed or outcome narrowed")
				}
				recovery, err := s.RecoverKV(l)
				if err != nil {
					t.Fatal(err)
				}
				fresh := recovery.Lease
				defer fresh.Close()
				if mode != "close" && (!recovery.Last.WriteOutcomeUnknown || recovery.Last.Request != prepared.Snapshot()) {
					t.Fatal("recovery lost original request or uncertainty")
				}
				if opened.Load() != 1 {
					t.Fatal("recovery authenticated or replayed automatically")
				}
				if _, err = fresh.Execute(context.Background(), prepared); !errors.Is(err, contract.ErrKVPlanUsed) {
					t.Fatal("foreign original preparation replayed on recovery")
				}
				if err = fresh.Preconnect(context.Background()); err != nil || opened.Load() != 2 {
					t.Fatal("explicit fresh preconnect failed")
				}
				if kind == "put" || kind == "txn" {
					observed := nativeKVDo(t, fresh, &pb.RangeRequest{Key: key})
					if observed.Reply.Entries[0].Version != 2 || !bytes.Equal(observed.Reply.Entries[0].Value, []byte("later-independent")) {
						t.Fatal("later write replaced by replay")
					}
				}
				if kind == "grant" {
					after := nativeDo(t, upstream, &pb.LeaseLeasesRequest{}).Native().(*pb.LeaseLeasesResponse)
					seen := map[int64]bool{}
					for _, x := range before.Leases {
						seen[x.ID] = true
					}
					added := []int64{}
					for _, x := range after.Leases {
						if !seen[x.ID] {
							added = append(added, x.ID)
						}
					}
					if len(added) != 1 || added[0] != f.receipt.(*pb.LeaseGrantResponse).ID {
						t.Fatal("grant allocated more than its original receipt")
					}
				}
				if kind == "keepalive" && (f.receipt.(*pb.LeaseKeepAliveResponse).ID != leaseID || f.receipt.(*pb.LeaseKeepAliveResponse).TTL < 1) {
					t.Fatal("missing actual keepalive receipt")
				}
				if kind == "revoke" && len(nativeKVDo(t, fresh, &pb.RangeRequest{Key: key}).Reply.Entries) != 0 {
					t.Fatal("revoke did not remove original attached key")
				}
				if f.calls.Load() != 1 {
					t.Fatal("old payload replayed during explicit recovery")
				}
			})
		}
	}
}
func TestEtcdNativeSessionOriginalReadBusyCancellationAndClose(t *testing.T) {
	p, secret := sessionProfile(t)
	upstream := nativeClient(t)
	for _, mode := range []string{"cancel", "lease_close", "session_close"} {
		t.Run(mode, func(t *testing.T) {
			c, f := faultClient(t, upstream, "/etcdserverpb.KV/Range", "read-block")
			s := newSession(p, secret, nativeClient(t))
			defer s.Close()
			s.opener = func(context.Context, connections.Profile, string) (*Client, error) { return c, nil }
			l := reserveNativeKV(t, s, 1)
			if err := l.Preconnect(context.Background()); err != nil {
				t.Fatal(err)
			}
			request := nativeKVRequest(t, &pb.RangeRequest{Key: []byte("/original-read")})
			plan, _ := l.Prepare(request)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			out := make(chan contract.KVResult, 1)
			go func() { r, _ := l.Execute(ctx, plan); out <- r }()
			waitSignal(t, f.entered)
			queued, _ := l.Prepare(request)
			if _, err := l.Execute(context.Background(), queued); !errors.Is(err, contract.ErrKVBusy) || queued.(*kvPreparation).used.Load() {
				t.Fatal("busy request queued or consumed")
			}
			if mode == "cancel" {
				cancel()
			} else if mode == "lease_close" {
				_ = l.Close()
			} else {
				_ = s.Close()
			}
			select {
			case r := <-out:
				if !r.Attempted || r.Completed || r.WriteOutcomeUnknown {
					t.Fatal("read cancellation became a write uncertainty")
				}
			case <-time.After(8 * time.Second):
				t.Fatal("read executor did not settle")
			}
			waitSignal(t, f.done)
			if c.Alive() {
				t.Fatal("original read channel remained usable")
			}
			nativeDo(t, upstream, &pb.RangeRequest{Key: []byte("/independent")})
		})
	}
}
func TestEtcdNativeSessionLateHandshakeClosesOnlyOriginalHandle(t *testing.T) {
	p, secret := sessionProfile(t)
	s := nativeSession(t)
	opened := make(chan *Client, 1)
	release := make(chan struct{})
	var released atomic.Bool
	defer func() {
		if released.CompareAndSwap(false, true) {
			close(release)
		}
	}()
	s.opener = func(ctx context.Context, p connections.Profile, secret string) (*Client, error) {
		c, err := OpenProfile(ctx, p, secret)
		if err == nil {
			opened <- c
			<-release
		}
		return c, err
	}
	l := reserveNativeKV(t, s, 1)
	finished := make(chan error, 1)
	go func() { finished <- l.Preconnect(context.Background()) }()
	var original *Client
	select {
	case original = <-opened:
	case <-time.After(8 * time.Second):
		t.Fatal("native original handshake not observed")
	}
	closed := make(chan error, 1)
	go func() { closed <- s.Close() }()
	waitSignal(t, s.ctx.Done())
	replacementRaw, err := OpenSession(context.Background(), p, secret)
	if err != nil {
		t.Fatal(err)
	}
	replacement := replacementRaw.(*Session)
	defer replacement.Close()
	select {
	case <-closed:
		t.Fatal("session close did not wait for captured handshake executor")
	default:
	}
	released.Store(true)
	close(release)
	select {
	case err = <-finished:
		if err == nil {
			t.Fatal("late native open accepted after close")
		}
	case <-time.After(8 * time.Second):
		t.Fatal("late original executor did not finish")
	}
	select {
	case err = <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("original session close did not finish")
	}
	if original.Alive() || !replacement.Alive() {
		t.Fatal("late open closed replacement or retained original")
	}
	nativeKVDo(t, reserveNativeKV(t, replacement, 1), &pb.RangeRequest{Key: []byte("/replacement")})
}

func TestEtcdNativeSessionLeaseLimitsAndRevokedTokenRecovery(t *testing.T) {
	s := nativeSession(t)
	if _, err := s.ReserveKV(0); err == nil {
		t.Fatal("zero document identity accepted")
	}
	for i := uint64(1); i <= 32; i++ {
		reserveNativeKV(t, s, i)
	}
	if _, err := s.ReserveKV(33); !errors.Is(err, contract.ErrKVLeaseLimit) {
		t.Fatal("lease bound not enforced")
	}
	var old contract.KVRequestLease
	for l := range s.leases {
		if l.document == 1 {
			old = l
			break
		}
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	l := reserveNativeKV(t, s, 33)
	if err := l.Preconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	p, _ := sessionProfile(t)
	if p.EtcdOptions.Auth == "basic" && p.EtcdOptions.ClientCertFile == "" {
		admin := nativeClient(t)
		if _, err := pb.NewAuthClient(admin.conn).AuthDisable(admin.authContext(context.Background()), &pb.AuthDisableRequest{}); err != nil {
			t.Fatal(err)
		}
		if _, err := pb.NewAuthClient(admin.conn).AuthEnable(context.Background(), &pb.AuthEnableRequest{}); err != nil {
			t.Fatal(err)
		}
		key := nativeKey(t)
		plan, err := l.Prepare(nativeKVRequest(t, &pb.PutRequest{Key: key, Value: []byte("revoked-original")}))
		if err != nil {
			t.Fatal(err)
		}
		r, err := l.Execute(context.Background(), plan)
		if err == nil || r.Code != "Unauthenticated" || r.Completed || r.WriteOutcomeUnknown || !errors.Is(l.CheckKVLease(), contract.ErrKVLeaseLost) {
			t.Fatal("native token automatically refreshed or rejection changed")
		}
		recovery, err := s.RecoverKV(l)
		if err != nil {
			t.Fatal(err)
		}
		defer recovery.Lease.Close()
		if recovery.Last.Request != plan.Snapshot() || recovery.Last.Code != "Unauthenticated" {
			t.Fatal("recovery lost definite original rejection")
		}
		observed := nativeKVDo(t, recovery.Lease, &pb.RangeRequest{Key: key})
		if len(observed.Reply.Entries) != 0 {
			t.Fatal("revoked original request replayed")
		}
	} else {
		// Anonymous and certificate modes have no password token to revoke.
		// Explicit replacement still must reject the old plan and keep the channel
		// unopened until the caller requests a new read.
		plan, err := l.Prepare(nativeKVRequest(t, &pb.RangeRequest{Key: []byte("/lease-limit")}))
		if err != nil {
			t.Fatal(err)
		}
		recovery, err := s.RecoverKV(l)
		if err != nil {
			t.Fatal(err)
		}
		defer recovery.Lease.Close()
		if _, err = recovery.Lease.Execute(context.Background(), plan); !errors.Is(err, contract.ErrKVPlanUsed) {
			t.Fatal("old plan accepted by fresh recovery")
		}
		nativeKVDo(t, recovery.Lease, &pb.RangeRequest{Key: []byte("/lease-limit")})
	}
}
