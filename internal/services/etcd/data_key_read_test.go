package etcd

import (
	"bytes"
	"context"
	"sync/atomic"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestDataKeyReadRejectsForeignAndExpiredOriginalObservationsBeforeIO(t *testing.T) {
	s := dataTestSession(t)
	doc, _ := s.ReserveDataDocument(s.KVScope().Namespace.Object("keyspace", "keys"))
	l := doc.(*dataLease)
	l.revision, l.pageStarted = 7, true
	l.access = &keyAccess{Read: []byteRange{{Start: bytes.Clone(s.prefix), End: []byte{0}}}}
	entry := contract.KVEntry{Key: append(bytes.Clone(s.prefix), 1), Value: []byte{0, 255}, CreateRevision: 2, ModRevision: 3, Version: 2}
	original := contract.KVKeyReadRequest{Snapshot: l.snapshot(entry, false), Cursor: l.boundary(s.prefix, 0, 100, 7, 0)}
	if _, _, err := l.keyReadObservation(original); err != nil {
		t.Fatal("valid original key observation rejected", err)
	}
	var opens atomic.Int32
	s.opener = func(context.Context, connections.Profile, string) (*Client, error) {
		opens.Add(1)
		return nil, ErrInvalid
	}
	for _, mode := range []string{"key", "snapshot", "cursor", "offset", "columns", "sort", "epoch"} {
		r := original.Clone()
		switch mode {
		case "key":
			r.Snapshot.Ref.Values[0][len(s.prefix)]++
		case "snapshot":
			r.Snapshot.Metadata += "x"
		case "cursor":
			r.Cursor.Values[0][0] ^= 1
		case "offset":
			r.Cursor.Offset++
		case "columns":
			r.Cursor.Columns[0] = "other"
		case "sort":
			r.Cursor.SortColumn = "value"
		case "epoch":
			l.epoch++
		}
		if _, err := l.captureKeyRead(context.Background(), s.ProfileID(), "kv", "keys", r); err == nil || opens.Load() != 0 {
			t.Fatal("invalid original key triggered I/O", mode, err)
		}
	}
}
