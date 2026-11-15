package tdengine

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func localDataSession(t *testing.T) *Session {
	t.Helper()
	p, _ := connections.Defaults(connections.TDengine)
	p.ID = 741
	ctx, cancel := context.WithCancel(context.Background())
	s := &Session{profile: p, ctx: ctx, cancel: cancel, metadataSlots: make(chan struct{}, 2), sqlSlots: make(chan struct{}, 2), closedDone: make(chan struct{})}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestDataDocumentReservationHasNoIOAndReclaimsOriginalLimit(t *testing.T) {
	s := localDataSession(t)
	o := s.Namespace("original").Object("table", "items")
	var first contract.DataDocumentLease
	for i := 0; i < 32; i++ {
		l, err := s.ReserveDataDocument(o)
		if err != nil {
			t.Fatal("no-I/O reservation failed", err)
		}
		if i == 0 {
			first = l
		}
	}
	if _, err := s.ReserveDataDocument(o); !errors.Is(err, contract.ErrSQLSessionLimit) {
		t.Fatal("document reservation exceeded original session limit", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReserveDataDocument(o); err != nil || len(s.dataLeases) != 32 {
		t.Fatal("closed original slot not reclaimed", err)
	}
	if err := s.Close(); err != nil || len(s.dataLeases) != 0 {
		t.Fatal("session retained document leases", err)
	}
	if _, err := s.ReserveDataDocument(o); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("closed original session reserved a document", err)
	}
}

func TestDataDocumentCloseCancelsAndWaitsForOriginalWorkers(t *testing.T) {
	s := localDataSession(t)
	o := s.Namespace("original").Object("table", "items")
	l := nativeDataDocument(t, s, o)
	var contexts []context.Context
	var finishes []func()
	acknowledged := false
	defer func() {
		if !acknowledged {
			for _, finish := range finishes {
				finish()
			}
		}
	}()
	for i := 0; i < 8; i++ {
		ctx, done, err := l.request(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		contexts, finishes = append(contexts, ctx), append(finishes, done)
	}
	if _, _, err := l.request(context.Background()); !errors.Is(err, contract.ErrSQLSessionLimit) {
		t.Fatal("document admitted more than eight original jobs", err)
	}
	closed := make(chan struct{})
	go func() { _ = l.Close(); close(closed) }()
	for _, ctx := range contexts {
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
			t.Fatal("original worker not canceled")
		}
	}
	select {
	case <-closed:
		t.Fatal("close released identity before executor acknowledgement")
	default:
	}
	for _, finish := range finishes {
		finish()
	}
	acknowledged = true
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("close did not settle acknowledged workers")
	}
	if l.key != [32]byte{} || l.meta != nil || len(s.dataLeases) != 0 {
		t.Fatal("closed document retained grants or metadata")
	}
	if _, _, err := l.request(context.Background()); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("closed document admitted work", err)
	}
}

func TestDataDocumentSignedRowsBindCompleteOriginalKeysAndFingerprints(t *testing.T) {
	s := localDataSession(t)
	o := s.Namespace("original").Object("table", "items")
	l, other := nativeDataDocument(t, s, o), nativeDataDocument(t, s, o)
	m := &dataMetadata{Columns: []contract.StructureColumn{{Name: "ts", DataType: "TIMESTAMP"}, {Name: "bin", DataType: "VARBINARY"}}, Keys: []int{0}, Signature: "original metadata", Epoch: 1, Precision: "ns"}
	cells := []contract.Cell{{Kind: contract.DateTime, Text: "2024-01-01T00:00:00.123456789Z"}, {Kind: contract.Binary, Bytes: []byte{0, 255}}}
	snapshot, err := l.snapshot(m, cells)
	if err != nil {
		t.Fatal(err)
	}
	cells[1].Bytes[0] = 1
	if !bytes.Equal(snapshot.Original[1].Bytes, []byte{0, 255}) {
		t.Fatal("complete original values followed caller bytes")
	}
	g, err := l.grant(snapshot.Ref)
	if err != nil || validateDataGrant(g, m, snapshot.Ref) != nil {
		t.Fatal("original signed row unavailable", err)
	}
	if _, err := other.grant(snapshot.Ref); !errors.Is(err, contract.ErrDataConflict) {
		t.Fatal("row grant crossed document ownership", err)
	}
	ref := contract.CloneDataMutationSnapshot(contract.DataMutationSnapshot{Edits: []contract.DataEdit{{Original: snapshot}}}).Edits[0].Original.Ref
	ref.Values[0][0] = '9'
	if _, err := l.grant(ref); !errors.Is(err, contract.ErrDataConflict) {
		t.Fatal("original key bytes were caller replaced", err)
	}
	changed := *m
	changed.Epoch++
	if !errors.Is(validateDataGrant(g, &changed, snapshot.Ref), contract.ErrDataConflict) {
		t.Fatal("old grant survived metadata epoch")
	}
	cells[1].Bytes = bytes.Repeat([]byte{255}, contract.DataEditMaxBytes+1)
	large, err := l.snapshot(m, cells)
	if err != nil || large.Mode != "fingerprints" || len(large.Original) != 0 || len(large.Fingerprints) != 2 {
		t.Fatal("oversized original row was narrowed to editable previews", err)
	}
	if reflect.DeepEqual(large.Fingerprints, snapshot.Fingerprints) {
		t.Fatal("complete bytes not included in original fingerprint")
	}
}

func TestDataTimestampLiteralPreservesPrecisionNegativeTimeAndRange(t *testing.T) {
	for _, test := range []struct{ text, precision, want string }{
		{"2024-01-01T00:00:00.123Z", "ms", "1704067200123"},
		{"2024-01-01T00:00:00.123456Z", "us", "1704067200123456"},
		{"2024-01-01T00:00:00.123456789Z", "ns", "1704067200123456789"},
		{"1969-12-31T23:59:59.999999999Z", "ns", "-1"},
		{"1969-12-31T23:59:59.999Z", "ms", "-1"},
	} {
		got, err := timestampLiteral(contract.Cell{Kind: contract.DateTime, Text: test.text}, test.precision)
		if err != nil || got != test.want {
			t.Fatal("timestamp identity rounded or overflowed", test, got, err)
		}
	}
	for _, test := range []struct{ text, precision string }{{"2024-01-01T00:00:00.123456789Z", "ms"}, {"9999-01-01T00:00:00Z", "ns"}, {"not a timestamp", "ns"}} {
		if _, err := timestampLiteral(contract.Cell{Kind: contract.DateTime, Text: test.text}, test.precision); !errors.Is(err, connections.ErrInvalid) {
			t.Fatal("unrepresentable original timestamp accepted", test, err)
		}
	}
}

func TestDataOperationErrorsRetainNeutralIdentityAndLimits(t *testing.T) {
	for _, err := range []error{contract.ErrDataConflict, contract.ErrDataEditLimit, contract.ErrDataReadOnly, contract.ErrDataPage, contract.ErrSQLSessionLimit} {
		if !errors.Is(safeError(err, "temporary secret"), err) {
			t.Fatal("original data error replaced by connection failure", err)
		}
	}
}
