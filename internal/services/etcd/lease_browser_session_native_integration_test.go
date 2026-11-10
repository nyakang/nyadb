package etcd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
)

func nativeLeaseBrowserSession(t *testing.T) *Session {
	t.Helper()
	if os.Getenv("NYADB_ETCD_LEASE_BROWSER_SESSION_IT") != "1" {
		t.Skip("requires owned original lease-browser session fixture")
	}
	return nativeSession(t)
}

func TestEtcdNativeLeaseBrowserSessionOriginalDocumentAndIndependentOneShotReceipts(t *testing.T) {
	s := nativeLeaseBrowserSession(t)
	admin := nativeClient(t)
	l := reserveNativeKV(t, s, 731).(*kvLease)
	grant := nativeDo(t, admin, &pb.LeaseGrantRequest{TTL: 60, ID: 9007199254745993}).Native().(*pb.LeaseGrantResponse)
	t.Cleanup(func() { nativeDo(t, admin, &pb.LeaseRevokeRequest{ID: grant.ID}) })
	key := append(nativeKey(t), 0, 255)
	nativeDo(t, admin, &pb.PutRequest{Key: key, Value: []byte{0, 255}, Lease: grant.ID})
	request := nativeKVRequest(t, &pb.PutRequest{Key: key, Value: []byte("one-shot")})
	plan, err := l.Prepare(request)
	if err != nil {
		t.Fatal(err)
	}
	one, err := l.Execute(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	before := l.LastResult()
	base := grant.ID - 1
	page, err := l.BrowseLeases(context.Background(), contract.KVLeasePageRequest{Limit: 1, After: &base})
	if err != nil || !page.Complete || len(page.Rows) != 1 || page.Rows[0].ID != grant.ID || page.Scope.Generation != 1 || page.Scope.Namespace != s.KVScope().Namespace || len(page.List.Native) == 0 {
		t.Fatal("original page scope or complete receipt lost", err)
	}
	if !bytes.Equal(l.LastResult().Native, before.Native) || l.LastResult().Request != one.Request {
		t.Fatal("browser replaced the independent one-shot receipt")
	}
	detail, err := l.ObserveLease(context.Background(), contract.KVLeaseDetailRequest{ID: grant.ID, IncludeKeys: true})
	if err != nil || !detail.Result.Completed || detail.Request.ID != grant.ID || detail.Scope.Namespace != page.Scope.Namespace || detail.Result.Reply == nil || detail.Result.Reply.Lease.ID != grant.ID {
		t.Fatal("original detail changed", err)
	}
	// Put without a lease detached this key. The lease observation stays actual.
	if len(detail.Result.Reply.Lease.Keys) != 0 {
		t.Fatal("historical attachment substituted for actual empty lease")
	}
	var opens atomic.Int32
	s.opener = func(context.Context, connections.Profile, string) (*Client, error) {
		opens.Add(1)
		return nil, ErrInvalid
	}
	recovery, err := s.RecoverKV(l)
	if err != nil || recovery.Lease == nil || opens.Load() != 0 || !bytes.Equal(recovery.Last.Native, before.Native) {
		t.Fatal("recovery read again or lost original one-shot receipt", err)
	}
	if _, ok := recovery.Lease.(contract.KVLeaseBrowsing); !ok {
		t.Fatal("fresh original lease lost optional browsing facet")
	}
}

func TestEtcdNativeLeaseBrowserSessionActualPartialReadCancelAndClose(t *testing.T) {
	admin := nativeLeaseBrowserSession(t).client
	for _, id := range []int64{9007199254746993, 9007199254746994} {
		nativeDo(t, admin, &pb.LeaseGrantRequest{TTL: 60, ID: id})
		t.Cleanup(func() { nativeDo(t, admin, &pb.LeaseRevokeRequest{ID: id}) })
	}
	for _, mode := range []string{"cancel", "close"} {
		t.Run(mode, func(t *testing.T) {
			s := nativeLeaseBrowserSession(t)
			l := reserveNativeKV(t, s, 732).(*kvLease)
			c, f := leaseBrowserProxy(t, admin, "/etcdserverpb.Lease/LeaseTimeToLive", mode, 2)
			l.client = c
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type result struct {
				page contract.KVLeasePage
				err  error
			}
			out := make(chan result, 1)
			base := int64(9007199254746992)
			go func() {
				p, e := l.BrowseLeases(ctx, contract.KVLeasePageRequest{Limit: 2, After: &base})
				out <- result{p, e}
			}()
			waitSignal(t, f.entered)
			if _, err := l.ObserveLease(context.Background(), contract.KVLeaseDetailRequest{ID: base + 1}); !errors.Is(err, contract.ErrKVBusy) {
				t.Fatal("parallel document read admitted", err)
			}
			if mode == "cancel" {
				cancel()
			} else {
				_ = l.Close()
			}
			var got result
			select {
			case got = <-out:
			case <-time.After(8 * time.Second):
				t.Fatal("original session read did not settle")
			}
			waitSignal(t, f.done)
			if got.err == nil || got.page.Complete || !got.page.List.Completed || len(got.page.Rows) != 2 || !got.page.Rows[0].Result.Completed || got.page.Rows[1].Result.Completed || got.page.Rows[1].Result.WriteOutcomeUnknown || f.ttlCalls.Load() != 2 {
				t.Fatal("original partial session receipts changed", got.err)
			}
			if _, err := l.BrowseLeases(context.Background(), contract.KVLeasePageRequest{Limit: 1}); err == nil {
				t.Fatal("original lost document reread automatically")
			}
		})
	}
}

func TestEtcdNativeLeaseBrowserSessionActualUnknownWriteRecoveryKeepsOriginalReceipt(t *testing.T) {
	s := nativeLeaseBrowserSession(t)
	admin := nativeClient(t)
	l := reserveNativeKV(t, s, 733).(*kvLease)
	grant := nativeDo(t, admin, &pb.LeaseGrantRequest{TTL: 60}).Native().(*pb.LeaseGrantResponse)
	t.Cleanup(func() { nativeDo(t, admin, &pb.LeaseRevokeRequest{ID: grant.ID}) })
	key := nativeKey(t)
	c, fault := faultClient(t, admin, "/etcdserverpb.KV/Put", "lost")
	l.client = c
	request := nativeKVRequest(t, &pb.PutRequest{Key: key, Value: []byte("original"), Lease: grant.ID})
	plan, err := l.Prepare(request)
	if err != nil {
		t.Fatal(err)
	}
	r, err := l.Execute(context.Background(), plan)
	waitSignal(t, fault.done)
	if err == nil || !r.Attempted || r.Completed || !r.WriteOutcomeUnknown || !l.LastResult().WriteOutcomeUnknown {
		t.Fatal("actual original processed write lost its independent unknown receipt", err)
	}
	recovery, err := s.RecoverKV(l)
	if err != nil || recovery.Lease == nil || !recovery.Last.WriteOutcomeUnknown || recovery.Last.Request != request {
		t.Fatal("pure recovery changed the original unknown write", err)
	}
	defer recovery.Lease.Close()
	browser := recovery.Lease.(contract.KVLeaseBrowsing)
	page, err := browser.BrowseLeases(context.Background(), contract.KVLeasePageRequest{Limit: 200})
	if err != nil || !page.Complete || !recovery.Last.WriteOutcomeUnknown {
		t.Fatal("later actual cluster list reclassified the unknown write", err)
	}
	detail, err := browser.ObserveLease(context.Background(), contract.KVLeaseDetailRequest{ID: grant.ID, IncludeKeys: true})
	if err != nil || !detail.Result.Completed || !recovery.Last.WriteOutcomeUnknown || recovery.Last.Request != request || recovery.Lease.LastResult().Attempted || fault.calls.Load() != 1 {
		t.Fatal("TTL detail replaced/replayed the original unknown write", err)
	}
	observed := nativeDo(t, admin, &pb.RangeRequest{Key: key}).Native().(*pb.RangeResponse)
	if len(observed.Kvs) != 1 || !bytes.Equal(observed.Kvs[0].Value, []byte("later-independent")) {
		t.Fatal("browser recovery replayed the original write")
	}
}
