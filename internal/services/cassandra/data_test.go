package cassandra

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"slices"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

// Native paging is permitted to yield short or empty pages with continuation.
// The synthetic server deliberately does both and overfills the remaining UI
// page. Assert every outbound PageSize remains the original HMAC-bound size.
func TestDataNativeShortEmptySpillAndRepeatPages(t *testing.T) {
	client, server := net.Pipe()
	c := &Client{conn: client, closeDone: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		defer server.Close()
		for {
			header := make([]byte, 9)
			if _, err := io.ReadFull(server, header); err != nil {
				done <- nil
				return
			}
			body := make([]byte, binary.BigEndian.Uint32(header[5:]))
			if _, err := io.ReadFull(server, body); err != nil {
				done <- err
				return
			}
			r := &reader{data: body}
			n := int(r.i32())
			r.take(n)
			r.u16()
			flags := r.u8()
			size := r.i32()
			state := ""
			if flags&8 != 0 {
				raw, _ := r.blob()
				state = string(raw)
			}
			if size != 3 || r.done() != nil {
				done <- errors.New("native page size changed")
				return
			}
			var rows []int32
			next := ""
			switch state {
			case "":
				rows = []int32{0}
				next = "A"
			case "A":
				next = "B"
			case "B":
				rows = []int32{1, 2, 3}
				next = "C"
			case "C":
				rows = []int32{4, 5, 6}
				next = "D"
			case "D":
				rows = []int32{7}
			}
			w := &writer{}
			w.i32(2)
			f := int32(1)
			if next != "" {
				f |= 2
			}
			w.i32(f)
			w.i32(1)
			if next != "" {
				w.blob([]byte(next))
			}
			w.str("original")
			w.str("rows")
			w.str("id")
			w.u16(9)
			w.i32(int32(len(rows)))
			for _, id := range rows {
				x := &writer{}
				x.i32(id)
				w.blob(x.Bytes())
			}
			header[0] = 0x84
			header[1] = 0
			header[4] = 8
			binary.BigEndian.PutUint32(header[5:], uint32(w.Len()))
			if _, err := server.Write(append(header, w.Bytes()...)); err != nil {
				done <- err
				return
			}
		}
	}()
	defer func() {
		c.Close()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	o := (contract.NamespaceRef{ProfileID: 1, Catalog: "original"}).Object("table", "rows")
	b := &dataBinding{Names: []string{"id"}}
	var cols []Column
	batch := func(p dataPosition) ([]string, dataPosition, bool) {
		t.Helper()
		var out []string
		n, more, err := dataBatch(context.Background(), c, o, b, p, 3, &cols, func(v []Value) error { out = append(out, v[0].Text); return nil })
		if err != nil {
			t.Fatal(err)
		}
		return out, n, more
	}
	first, next, more := batch(dataPosition{})
	if !slices.Equal(first, []string{"0", "1", "2"}) || !more || string(next.State) != "B" || next.Skip != 2 {
		t.Fatal("short/empty fill/spill", first, next)
	}
	second, last, more := batch(next)
	if !slices.Equal(second, []string{"3", "4", "5"}) || !more || string(last.State) != "C" || last.Skip != 2 {
		t.Fatal("continuation", second, last)
	}
	repeated, _, _ := batch(next)
	if !slices.Equal(second, repeated) {
		t.Fatal("repeat lost original native boundary")
	}
	final, _, more := batch(last)
	if !slices.Equal(final, []string{"6", "7"}) || more {
		t.Fatal("last live page", final)
	}
	bad := next
	bad.Skip = 4
	if _, _, err := dataBatch(context.Background(), c, o, b, bad, 3, &cols, func([]Value) error { return nil }); !errors.Is(err, contract.ErrDataConflict) {
		t.Fatal("live spill shrink accepted", err)
	}
}

func TestDataReservationBoundsAndUnsupportedRulesHaveNoIO(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &Session{profile: connections.Profile{ID: 1}, ctx: ctx, datas: map[*dataLease]bool{}}
	o := (contract.NamespaceRef{ProfileID: 1, Catalog: "original"}).Object("table", "rows")
	var leases []*dataLease
	defer func() {
		for _, l := range leases {
			l.Close()
		}
	}()
	for range 32 {
		v, err := s.ReserveDataDocument(o)
		if err != nil {
			t.Fatal(err)
		}
		leases = append(leases, v.(*dataLease))
	}
	if _, err := s.ReserveDataDocument(o); !errors.Is(err, ErrBounds) {
		t.Fatal("unbounded data documents", err)
	}
	if _, err := s.ReserveDataDocument(o.Namespace().Object("type", "rows")); !errors.Is(err, contract.ErrUnsupported) {
		t.Fatal("UDT became a data table", err)
	}
	l := leases[0]
	options := contract.DataOptions{Object: o, Limit: 20, FilterColumn: "id", FilterMode: "equal", FilterValue: "1"}
	if _, err := l.page(ctx, o.ProfileID, o.Catalog, o.Name, options); !errors.Is(err, contract.ErrUnsupported) {
		t.Fatal("unsupported filter dispatched", err)
	}
	canceled, stop := context.WithCancel(ctx)
	stop()
	if _, err := l.page(canceled, o.ProfileID, o.Catalog, o.Name, contract.DataOptions{Object: o, Limit: 20}); !errors.Is(err, context.Canceled) {
		t.Fatal("pre-canceled page dispatched", err)
	}
	for _, l := range leases {
		if l.pageClient != nil || l.binding != nil {
			t.Fatal("reservation/validation performed I/O")
		}
	}
	if err := leases[0].Close(); err != nil {
		t.Fatal(err)
	}
	if v, err := s.ReserveDataDocument(o); err != nil {
		t.Fatal("closed reservation retained slot", err)
	} else {
		leases = append(leases, v.(*dataLease))
	}
}

func TestDataIdentityEmptyBinaryKeysAndNullFingerprints(t *testing.T) {
	l := &dataLease{object: (contract.NamespaceRef{ProfileID: 1, Catalog: "original"}).Object("table", "rows")}
	b := &dataBinding{ID: "uuid", Signature: "schema", Names: []string{"key", "payload"}, Keys: []string{"key"}, Partition: []string{"key"}}
	values := []Value{{Type: &Type{Code: 3}, Raw: []byte{}}, {Type: &Type{Code: 10}, Raw: []byte{}, Text: ""}}
	snapshot, err := l.snapshot(values, b)
	if err != nil {
		t.Fatal(err)
	}
	cloned := snapshot.Ref
	cloned.Values = [][]byte{bytes.Clone(cloned.Values[0])}
	cloned.TypedValues = []contract.Cell{snapshot.Ref.TypedValues[0].Clone()}
	identity, err := l.decodeRef(cloned)
	if err != nil || identity.Parameters[0].Null || identity.Parameters[0].Bytes == nil || identity.Typed[0].Kind != contract.Binary {
		t.Fatal("empty binary key identity", err)
	}
	// Cell JSON omits empty []byte. Identity HMAC and byte comparison preserve the
	// explicit Binary kind across the worker clone and JSON envelope round trip.
	raw, _ := json.Marshal(identity)
	if len(raw) == 0 {
		t.Fatal("identity missing")
	}
	n := Value{Type: &Type{Code: 10}, Null: true}
	m := Value{Type: &Type{Code: 10}, Missing: true}
	if nativeFingerprint(n) == nativeFingerprint(values[1]) || nativeFingerprint(m) == nativeFingerprint(n) || nativeFingerprint(m) == nativeFingerprint(values[1]) {
		t.Fatal("null/missing/empty conflated")
	}
	cloned.TypedValues[0].Kind = contract.Null
	if _, err = l.decodeRef(cloned); !errors.Is(err, ErrInvalid) {
		t.Fatal("typed identity tampering accepted", err)
	}
}
