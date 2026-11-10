package etcd

import (
	"bytes"
	"context"
	"errors"
	"math"
	"sync/atomic"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/protobuf/proto"
)

func renewalTestLease(t *testing.T, s *Session, document uint64) *renewalLease {
	t.Helper()
	l, err := s.ReserveKVRenewal(document)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l.(*renewalLease)
}
func TestRenewalSessionPureReservationAuthorizationAndMixedDocumentLimit(t *testing.T) {
	s := eventsTestSession(t)
	var opens atomic.Int32
	s.opener = func(context.Context, connections.Profile, string) (*Client, error) {
		opens.Add(1)
		return nil, ErrInvalid
	}
	if _, err := s.ReserveKVRenewal(0); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	a := renewalTestLease(t, s, 1)
	r := contract.KVRenewalRequest{LeaseID: math.MaxInt64}
	p, err := a.Prepare(r)
	if err != nil || p.Snapshot() != r {
		t.Fatal("native authorization narrowed", err)
	}
	r.LeaseID = 1
	if p.Snapshot().LeaseID != math.MaxInt64 {
		t.Fatal("request aliases input")
	}
	b := renewalTestLease(t, s, 2)
	if _, err = b.Start(context.Background(), p); !errors.Is(err, contract.ErrKVPlanUsed) {
		t.Fatal("foreign plan admitted", err)
	}
	var nilPlan *renewalPreparation
	if _, err = a.Start(context.Background(), nilPlan); !errors.Is(err, contract.ErrKVPlanUsed) {
		t.Fatal("nil plan admitted", err)
	}
	for i := uint64(3); i <= 4; i++ {
		renewalTestLease(t, s, i)
	}
	if _, err = s.ReserveKVRenewal(5); !errors.Is(err, contract.ErrKVLeaseLimit) {
		t.Fatal("fifth run reservation admitted", err)
	}
	for i := uint64(1); i <= 4; i++ {
		eventsTestLease(t, s, i)
	}
	for i := uint64(1); i <= 24; i++ {
		if _, err = s.ReserveKV(i); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.ReserveKV(25); !errors.Is(err, contract.ErrKVLeaseLimit) {
		t.Fatal("mixed cap bypassed", err)
	}
	if _, err = s.ReserveDataDocument(s.KVScope().Namespace.Object("keyspace", "keys")); !errors.Is(err, contract.ErrKVLeaseLimit) {
		t.Fatal("data cap bypassed", err)
	}
	if opens.Load() != 0 {
		t.Fatal("reserve/prepare performed I/O")
	}
}
func TestRenewalSessionRetainedBudgetSharesEventsAndSurvivesRecovery(t *testing.T) {
	s := eventsTestSession(t)
	a, b := renewalTestLease(t, s, 1), renewalTestLease(t, s, 2)
	event := eventsTestLease(t, s, 1)
	if !a.admit(2<<20) || !b.admit(2<<20) || !event.admit(4<<20) || s.retainedBytes != 32<<20 {
		t.Fatal("mixed retention not charged")
	}
	if a.admit(1) || event.admit(1) {
		t.Fatal("retained budget bypassed")
	}
	// A new lease must inherit already-transferred receipts without freeing space.
	recovery, err := s.RecoverKVRenewal(a)
	if err != nil || recovery.Lease == nil || s.retainedBytes != 32<<20 || a.charged != 0 {
		t.Fatal("recovery released retained receipts", err)
	}
	fresh := recovery.Lease.(*renewalLease)
	defer fresh.Close()
	if fresh.charged != 8<<20 || fresh.admit(1) {
		t.Fatal("document recovery bypassed retained limit")
	}
	if err = fresh.ClearReceipts(); err != nil || s.retainedBytes != 24<<20 {
		t.Fatal("explicit disposal did not release inherited receipts", err)
	}
	if !b.admit(0) {
		t.Fatal("zero charge rejected")
	}
	// Cross-session recovery carries the original charge to the new session.
	c := renewalTestLease(t, s, 3)
	if !c.admit(1 << 20) {
		t.Fatal("admission failed after disposal")
	}
	other := eventsTestSession(t)
	moved, err := other.RecoverKVRenewal(c)
	if err != nil || other.retainedBytes != 4<<20 || c.charged != 0 || s.retainedBytes != 24<<20 {
		t.Fatal("cross-session recovery lost/duplicated budget", err)
	}
	defer moved.Lease.Close()
}
func TestRenewalSessionRecoveryBudgetFailureKeepsOriginalChargeAndRejectsForeignScope(t *testing.T) {
	first, second := eventsTestSession(t), eventsTestSession(t)
	a := renewalTestLease(t, first, 1)
	if !a.admit(1 << 20) {
		t.Fatal("charge failed")
	}
	eventsTestLease(t, second, 1).admit(4 << 20)
	eventsTestLease(t, second, 2).admit(4 << 20)
	if _, err := second.RecoverKVRenewal(a); !errors.Is(err, ErrBounds) || first.retainedBytes != 4<<20 || a.charged != 4<<20 {
		t.Fatal("failed recovery released original charge", err)
	}
	second.profile.EtcdOptions.Prefix = "foreign"
	second.prefix = []byte("foreign")
	b := renewalTestLease(t, first, 2)
	if _, err := second.RecoverKVRenewal(b); !errors.Is(err, contract.ErrRevoked) || b.CheckKVLease() != nil {
		t.Fatal("foreign recovery mutated original", err)
	}
	var nilLease *renewalLease
	if _, err := first.RecoverKVRenewal(nilLease); !errors.Is(err, contract.ErrKVPlanUsed) {
		t.Fatal("typed nil recovered", err)
	}
}
func TestRenewalSessionRetirementKeepsParkedOriginalUnknownReceiptsAndBarrier(t *testing.T) {
	s := eventsTestSession(t)
	l := renewalTestLease(t, s, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	close(done)
	raw := &RenewalRun{ctx: ctx, cancel: cancel, done: done, ended: true, terminal: RenewalTerminal{Reason: "interrupted", Attempts: 2, Acknowledged: 1, WriteOutcomeUnknown: true}}
	native := &pb.LeaseKeepAliveResponse{Header: watchTestHeader(7), ID: math.MaxInt64, TTL: 3}
	native.ProtoReflect().SetUnknown([]byte{0xa0, 0x06, 0x07})
	encoded, err := proto.Marshal(native)
	if err != nil {
		t.Fatal(err)
	}
	raw.reserve = l.admit
	raw.append(RenewalReceipt{Sequence: 1, Native: encoded, LeaseID: math.MaxInt64, TTL: 3, Header: header(native.Header), Attempted: true, Completed: true})
	raw.append(RenewalReceipt{Sequence: 2, LeaseID: math.MaxInt64, Attempted: true, WriteOutcomeUnknown: true})
	l.run = &renewalRun{raw: raw, request: contract.KVRenewalRequest{LeaseID: math.MaxInt64}}
	charged := s.retainedBytes
	if err = s.Close(); err != nil || s.retainedBytes != charged {
		t.Fatal("retirement discarded parked budget", err)
	}
	batch := l.run.Poll()
	if !batch.Ended || len(batch.Receipts) != 2 || !batch.Terminal.WriteOutcomeUnknown || s.retainedBytes != charged {
		t.Fatal("parked receipts changed")
	}
	original := &pb.LeaseKeepAliveResponse{}
	if proto.Unmarshal(batch.Receipts[0].Native, original) != nil || original.ID != math.MaxInt64 || !bytes.Equal(original.ProtoReflect().GetUnknown(), native.ProtoReflect().GetUnknown()) {
		t.Fatal("unknown native fields lost")
	}
	if _, err = l.Prepare(contract.KVRenewalRequest{LeaseID: math.MaxInt64}); !errors.Is(err, contract.ErrKVRenewalUncertain) {
		t.Fatal("unknown outcome did not block writes", err)
	}
	freshSession := eventsTestSession(t)
	recovery, err := freshSession.RecoverKVRenewal(l)
	if err != nil || recovery.Lease == nil || len(recovery.Batch.Receipts) != 0 || !recovery.Batch.Terminal.WriteOutcomeUnknown || recovery.Batch.Request.LeaseID != math.MaxInt64 {
		t.Fatal("recovery replayed/lost transferred terminal", err)
	}
	defer recovery.Lease.Close()
	if _, err = recovery.Lease.Prepare(contract.KVRenewalRequest{LeaseID: 1}); !errors.Is(err, contract.ErrKVRenewalUncertain) {
		t.Fatal("recovery erased unknown barrier", err)
	}
	if err = recovery.Lease.ClearReceipts(); !errors.Is(err, contract.ErrKVRenewalUncertain) {
		t.Fatal("clear erased unknown barrier", err)
	}
	again, err := freshSession.RecoverKVRenewal(recovery.Lease)
	if err != nil || !again.Batch.Terminal.WriteOutcomeUnknown || again.Batch.Request.LeaseID != math.MaxInt64 {
		t.Fatal("second recovery reclassified original", err)
	}
	defer again.Lease.Close()
}
