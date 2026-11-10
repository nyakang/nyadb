package dbruntime_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/protobuf/proto"
)

func TestEtcdNativeLeaseBrowserRuntimeOriginalPublicationAndPureRecovery(t *testing.T) {
	if os.Getenv("NYADB_ETCD_LEASE_BROWSER_SESSION_IT") != "1" {
		t.Skip("requires owned original browser runtime fixture")
	}
	p, secret := etcdRuntimeProfile(t)
	m := etcdRuntimeManager(t)
	published, err := m.ConnectSession(context.Background(), p, secret)
	if err != nil {
		t.Fatal(err)
	}
	writer := etcdRuntimeReserve(t, m, 1)
	grant := etcdRuntimeDo(t, writer, "Lease/LeaseGrant", &pb.LeaseGrantRequest{ID: 9007199254747993, TTL: 60})
	key := append([]byte("/lease-browser-runtime/"+t.Name()), 0, 255)
	etcdRuntimeDo(t, writer, "KV/Put", &pb.PutRequest{Key: key, Value: []byte{0, 255}, Lease: grant.Reply.Lease.ID})
	original := etcdRuntimeReserve(t, m, 2)
	browser, ok := original.(contract.KVLeaseBrowsing)
	if !ok {
		t.Fatal("published original lost browsing facet")
	}
	base := grant.Reply.Lease.ID - 1
	page, err := browser.BrowseLeases(context.Background(), contract.KVLeasePageRequest{Limit: 1, After: &base})
	if err != nil || !page.Complete || page.Scope.Generation != published.Generation() || len(page.Rows) != 1 || page.Rows[0].ID != grant.Reply.Lease.ID || page.Rows[0].Result.WriteOutcomeUnknown {
		t.Fatal("original published page changed", err)
	}
	detail, err := browser.ObserveLease(context.Background(), contract.KVLeaseDetailRequest{ID: grant.Reply.Lease.ID, IncludeKeys: true})
	native := &pb.LeaseTimeToLiveResponse{}
	if err != nil || !detail.Result.Completed || detail.Scope.Generation != published.Generation() || proto.Unmarshal(detail.Result.Native, native) != nil || native.ID != grant.Reply.Lease.ID || len(native.Keys) != 1 {
		t.Fatal("complete original published detail changed", err)
	}
	replacement, err := m.ConnectSession(context.Background(), p, secret)
	if err != nil {
		t.Fatal(err)
	}
	if replacement.Generation() <= page.Scope.Generation || !errors.Is(original.CheckKVLease(), contract.ErrRevoked) {
		t.Fatal("browser followed replacement publication")
	}
	provider, err := m.KVRequests(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	recovery, err := provider.RecoverKV(original)
	if err != nil || recovery.Lease == nil || recovery.Lease.Scope().Generation != replacement.Generation() || recovery.Last.Attempted {
		t.Fatal("browser recovery selected another identity or invented a native request", err)
	}
	defer recovery.Lease.Close()
	fresh, ok := recovery.Lease.(contract.KVLeaseBrowsing)
	if !ok {
		t.Fatal("recovery lost optional facet")
	}
	if _, err := browser.ObserveLease(context.Background(), detail.Request); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("old observation dispatched after replacement", err)
	}
	observed, err := fresh.ObserveLease(context.Background(), detail.Request)
	if err != nil || !observed.Result.Completed || observed.Scope.Generation != replacement.Generation() || observed.Result.Reply == nil || observed.Result.Reply.Lease.ID != native.ID {
		t.Fatal("explicit fresh observation failed", err)
	}
	// The parked old scope remains the old public generation after recovery.
	if page.Scope.Generation != published.Generation() || detail.Scope.Generation != published.Generation() || len(native.Keys) != 1 {
		t.Fatal("cached original page/detail relabeled by recovery")
	}
	cleanup := etcdRuntimeReserve(t, m, 3)
	etcdRuntimeDo(t, cleanup, "Lease/LeaseRevoke", &pb.LeaseRevokeRequest{ID: native.ID})
}
