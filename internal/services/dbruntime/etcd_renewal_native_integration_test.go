package dbruntime_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/protobuf/proto"
)

func TestEtcdNativeRenewalRuntimeOriginalPublicationParkedReceiptsAndExplicitRecovery(t *testing.T) {
	if os.Getenv("NYADB_ETCD_RENEWAL_SESSION_IT") != "1" {
		t.Skip("requires owned original renewal runtime fixture")
	}
	p, secret := etcdRuntimeProfile(t)
	m := etcdRuntimeManager(t)
	published, err := m.ConnectSession(context.Background(), p, secret)
	if err != nil {
		t.Fatal(err)
	}
	writer := etcdRuntimeReserve(t, m, 1)
	grant := etcdRuntimeDo(t, writer, "Lease/LeaseGrant", &pb.LeaseGrantRequest{ID: 9007199254740993, TTL: 3})
	key := append([]byte("/renewal-runtime/"+t.Name()), 0, 255)
	etcdRuntimeDo(t, writer, "KV/Put", &pb.PutRequest{Key: key, Value: []byte{0, 255}, Lease: grant.Reply.Lease.ID})
	provider, err := m.KVRenewal(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	l, err := provider.ReserveKVRenewal(1)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if l.Scope().Generation != published.Generation() {
		t.Fatal("renewal not bound to public generation")
	}
	request := contract.KVRenewalRequest{LeaseID: grant.Reply.Lease.ID}
	plan, err := l.Prepare(request)
	if err != nil {
		t.Fatal(err)
	}
	r, err := l.Start(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	// Let this original executor finish a second acknowledgement while parked.
	time.Sleep(1500 * time.Millisecond)
	replacement, err := m.ConnectSession(context.Background(), p, secret)
	if err != nil {
		t.Fatal(err)
	}
	if replacement.Generation() <= published.Generation() || !errors.Is(l.CheckKVLease(), contract.ErrRevoked) {
		t.Fatal("replacement reused original renewal")
	}
	batch := r.Poll()
	if !batch.Ended || len(batch.Receipts) < 2 || batch.Request != request || batch.Terminal.WriteOutcomeUnknown || batch.Terminal.Attempts != batch.Terminal.Acknowledged {
		t.Fatal("retirement discarded/reclassified acknowledged parked receipts", batch.Terminal)
	}
	for _, receipt := range batch.Receipts {
		native := &pb.LeaseKeepAliveResponse{}
		if proto.Unmarshal(receipt.Native, native) != nil || native.ID != request.LeaseID || native.TTL != receipt.TTL || !receipt.Completed || receipt.WriteOutcomeUnknown {
			t.Fatal("parked native receipt changed")
		}
	}
	fresh, err := m.KVRenewal(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	recovery, err := fresh.RecoverKVRenewal(l)
	if err != nil || recovery.Lease == nil || recovery.Lease.Scope().Generation != replacement.Generation() || recovery.Batch.Request != request || recovery.Batch.Terminal.WriteOutcomeUnknown || len(recovery.Batch.Receipts) != 0 {
		t.Fatal("recovery substituted/duplicated receipts", err)
	}
	defer recovery.Lease.Close()
	if _, err = recovery.Lease.Start(context.Background(), plan); !errors.Is(err, contract.ErrKVPlanUsed) {
		t.Fatal("old authorization replayed", err)
	}
	if err = recovery.Lease.Preconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	reader := etcdRuntimeReserve(t, m, 2)
	deadline := time.Now().Add(8 * time.Second)
	expired := false
	for time.Now().Before(deadline) {
		current := etcdRuntimeDo(t, reader, "KV/Range", &pb.RangeRequest{Key: key})
		if len(current.Reply.Entries) == 0 {
			expired = true
			break
		}
		time.Sleep(40 * time.Millisecond)
	}
	if !expired {
		t.Fatal("recovery/preconnect resumed original renewal")
	}
	if err = recovery.Lease.ClearReceipts(); err != nil {
		t.Fatal(err)
	}
}
