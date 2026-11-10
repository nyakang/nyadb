package etcd

import (
	"bytes"
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func dataTestSession(t *testing.T) *Session {
	t.Helper()
	p, _ := connections.Defaults(connections.Etcd)
	p.ID = 1
	p.EtcdOptions.PrefixEncoding, p.EtcdOptions.Prefix = "base64", "AP8="
	client := sessionIdentityClient(2)
	// Pure preview fixtures supply explicit captured all-key permissions.
	client.access = &keyAccess{Read: prefixRanges(nil), Write: prefixRanges(nil), ObservedRevision: 1}
	s := newSession(p, "", client)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestDataSessionSharesLogicalLimitAndCloseWaitsForOriginalWorkers(t *testing.T) {
	s := dataTestSession(t)
	o := s.KVScope().Namespace.Object("keyspace", "keys")
	for i := 0; i < 16; i++ {
		if _, err := s.ReserveKV(uint64(i + 1)); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ReserveDataDocument(o); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.ReserveKV(99); err == nil {
		t.Fatal("KV exceeded combined logical limit")
	}
	if _, err := s.ReserveDataDocument(o); err == nil {
		t.Fatal("data exceeded combined logical limit")
	}
	var original *dataLease
	for l := range s.dataDocuments {
		original = l
		break
	}
	var finish []func()
	var contexts []context.Context
	for i := 0; i < 8; i++ {
		ctx, done, err := original.begin(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		finish = append(finish, done)
		contexts = append(contexts, ctx)
	}
	if _, _, err := original.begin(context.Background()); err == nil {
		t.Fatal("worker bound exceeded")
	}
	closed := make(chan error, 1)
	go func() { closed <- s.Close() }()
	select {
	case <-contexts[0].Done():
	case <-time.After(time.Second):
		t.Fatal("close did not cancel original workers")
	}
	select {
	case <-closed:
		t.Fatal("close completed before acknowledgement")
	default:
	}
	for _, done := range finish {
		done()
	}
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("close did not wait/finish")
	}
	if len(s.dataDocuments) != 0 || len(s.leases) != 0 || original.key != ([32]byte{}) {
		t.Fatal("close retained logical leases/signing key")
	}
}
func TestDataReservationAndUnknownCursorDoNotOpenNativeChannels(t *testing.T) {
	s := dataTestSession(t)
	var opens atomic.Int32
	s.opener = func(context.Context, connections.Profile, string) (*Client, error) {
		opens.Add(1)
		return nil, ErrInvalid
	}
	o := s.KVScope().Namespace.Object("keyspace", "keys")
	raw, err := s.ReserveDataDocument(o)
	if err != nil {
		t.Fatal(err)
	}
	l := raw.(*dataLease)
	for _, scope := range l.scopes {
		if scope.client != nil {
			t.Fatal("reservation opened channel")
		}
	}
	for _, options := range []contract.DataOptions{{Offset: 100, Limit: 10}, {Offset: 10, Limit: 10, After: &contract.DataCursor{Offset: 10, Database: "kv", Table: "keys", Columns: []string{"etcd_mvcc"}, Values: [][]byte{[]byte(`{"body":{},"mac":"foreign"}`)}}}} {
		if _, err = l.page(context.Background(), o.ProfileID, o.Catalog, o.Name, options); err == nil || opens.Load() != 0 {
			t.Fatal("unknown page dispatched")
		}
	}
	if _, err = s.ReserveDataDocument(contract.ObjectRef{ProfileID: 1, Catalog: "kv", Schema: "prefix", Kind: "keyspace", Name: "keys"}); err == nil {
		t.Fatal("prefix was accepted as schema identity")
	}
	if err = l.Close(); err != nil {
		t.Fatal(err)
	}
	if len(s.dataDocuments) != 0 || l.key != ([32]byte{}) {
		t.Fatal("close retained original document/key")
	}
	if err = l.Close(); err != nil {
		t.Fatal("idempotent close", err)
	}
}
func TestDataSnapshotOwnsExactBytesAndRejectsForeignDocument(t *testing.T) {
	s := dataTestSession(t)
	o := s.KVScope().Namespace.Object("keyspace", "keys")
	a, _ := s.ReserveDataDocument(o)
	b, _ := s.ReserveDataDocument(o)
	first, second := a.(*dataLease), b.(*dataLease)
	entry := contract.KVEntry{Key: []byte{0, 255, 1}, Value: []byte{0, 255, 0, 1}, CreateRevision: 9007199254740993, ModRevision: 9007199254740994, Version: 5, Lease: 9007199254740995}
	snapshot := first.snapshot(entry, true)
	entry.Key[0] = 9
	entry.Value[0] = 9
	if !bytes.Equal(snapshot.Ref.Values[0], []byte{0, 255, 1}) || !bytes.Equal(snapshot.Original[1].Bytes, []byte{0, 255, 0, 1}) || snapshot.Original[5].Text != "9007199254740995" {
		t.Fatal("original typed bytes or int64 changed")
	}
	var observation dataObservation
	if err := first.decode([]byte(snapshot.Metadata), &observation); err != nil || observation.ModRevision != 9007199254740994 {
		t.Fatal("original signed observation missing", err)
	}
	if err := second.decode([]byte(snapshot.Metadata), &observation); err == nil {
		t.Fatal("snapshot crossed original lease")
	}
	snapshot.Ref.TypedValues[0].Bytes[0] = 9
	if err := first.validateRef(snapshot.Ref); err == nil {
		t.Fatal("typed/raw identity mismatch accepted")
	}
}
