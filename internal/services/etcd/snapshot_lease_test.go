package etcd

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func snapshotUnitLease(t *testing.T) (*Session, *kvLease, *atomic.Int32) {
	t.Helper()
	p, _ := connections.Defaults(connections.Etcd)
	p.ID = 1
	s := newSession(p, "", sessionIdentityClient(2))
	t.Cleanup(func() { s.Close() })
	opens := &atomic.Int32{}
	s.opener = func(context.Context, connections.Profile, string) (*Client, error) {
		opens.Add(1)
		return snapshotUnitClient(t, &snapshotTestServer{frames: snapshotUnitFrames()}), nil
	}
	l, err := s.ReserveKV(1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return s, l.(*kvLease), opens
}
func snapshotUnitRequest() contract.KVSnapshotRequest {
	return contract.KVSnapshotRequest{BodyBytes: 1024, JournalBytes: 65536, Frames: 20}
}
func TestSnapshotLeaseOriginalPurePreparationCopiedPlanAndSeparateUnknownReceipt(t *testing.T) {
	s, l, opens := snapshotUnitLease(t)
	p, err := l.PrepareSnapshot(snapshotUnitRequest())
	if err != nil || opens.Load() != 0 {
		t.Fatal("pure preparation performed IO", err)
	}
	copied := reflect.New(reflect.TypeOf(p).Elem())
	copied.Elem().Set(reflect.ValueOf(p).Elem())
	var body, journal bytes.Buffer
	outputs := contract.KVSnapshotOutputs{Snapshot: &body, Journal: &journal}
	if _, err := l.ExecuteSnapshot(context.Background(), copied.Interface().(contract.KVSnapshotPreparation), outputs); !errors.Is(err, contract.ErrKVPlanUsed) || opens.Load() != 0 {
		t.Fatal("copied plan gained original authority")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := l.ExecuteSnapshot(ctx, p, outputs); !errors.Is(err, context.Canceled) || opens.Load() != 0 {
		t.Fatal("canceled original dispatched")
	}
	l.writeUnknown = true
	l.last = contract.KVResult{WriteOutcomeUnknown: true, Native: []byte{0, 255}}
	r, err := l.ExecuteSnapshot(context.Background(), p, outputs)
	if err != nil || !r.Completed || r.Scope.DocumentID != 1 || r.Scope.Original.Namespace.ProfileID != s.profile.ID || opens.Load() != 1 || !l.writeUnknown || !l.LastResult().WriteOutcomeUnknown || !bytes.Equal(l.LastResult().Native, []byte{0, 255}) {
		t.Fatal("snapshot changed original unknown write evidence", err)
	}
	if l.client.LastSnapshot().Ended {
		t.Fatal("raw client retains duplicate document snapshot cache")
	}
	r.Checksum[0] ^= 255
	if bytes.Equal(l.LastSnapshotResult().Checksum, r.Checksum) {
		t.Fatal("document snapshot receipt aliases caller")
	}
	if l.snapshotCharged <= 0 || s.snapshotRetainedBytes != l.snapshotCharged {
		t.Fatal("snapshot metadata not charged")
	}
	if _, err := l.ExecuteSnapshot(context.Background(), p, outputs); !errors.Is(err, contract.ErrKVPlanUsed) {
		t.Fatal("original native Snapshot plan replayed")
	}
	recovered, err := s.RecoverKV(l)
	if err != nil || recovered.SnapshotResult == nil || !recovered.SnapshotResult.Completed || !recovered.Last.WriteOutcomeUnknown || opens.Load() != 1 || s.snapshotRetainedBytes != 0 || l.LastSnapshotResult().Ended {
		t.Fatal("recovery sent IO or lost original receipt/budget", err)
	}
	defer recovered.Lease.Close()
	fresh := recovered.Lease.(*kvLease)
	if !fresh.writeUnknown {
		t.Fatal("snapshot read settled unknown write during recovery")
	}
	if _, err := fresh.ExecuteSnapshot(context.Background(), p, outputs); !errors.Is(err, contract.ErrKVPlanUsed) {
		t.Fatal("recovery regained old Snapshot plan")
	}
}
func TestSnapshotLeaseMetadataReservationBoundsBeforeOpeningOriginalTransport(t *testing.T) {
	s, l, opens := snapshotUnitLease(t)
	p, _ := l.PrepareSnapshot(snapshotUnitRequest())
	var body, journal bytes.Buffer
	s.snapshotRetainedBytes = snapshotMetadataSessionBytes
	if _, err := l.ExecuteSnapshot(context.Background(), p, contract.KVSnapshotOutputs{Snapshot: &body, Journal: &journal}); !errors.Is(err, ErrBounds) || opens.Load() != 0 || l.snapshotCharged != 0 {
		t.Fatal("budget failure performed IO or consumed cache", err)
	}
	s.snapshotRetainedBytes = 0
	if _, err := l.ExecuteSnapshot(context.Background(), p, contract.KVSnapshotOutputs{Snapshot: &body, Journal: &journal}); err != nil {
		t.Fatal("budget denial consumed original un-dispatched plan", err)
	}
	l.Close()
	if s.snapshotRetainedBytes != 0 || l.LastSnapshotResult().Ended {
		t.Fatal("close did not release original metadata")
	}
}
