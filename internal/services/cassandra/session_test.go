package cassandra

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestCassandraReservationBoundsIdentityAndPrecancelHaveNoIO(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &Session{profile: connections.Profile{ID: 7, Kind: connections.Cassandra}, ctx: ctx, cancel: cancel, leases: map[*queryLease]bool{}}
	if _, err := s.ReserveQuery(0); err == nil {
		t.Fatal("zero document accepted")
	}
	var leases []contract.QueryLease
	for i := 1; i <= 32; i++ {
		l, err := s.ReserveQuery(uint64(i))
		if err != nil {
			t.Fatal(err)
		}
		leases = append(leases, l)
		if l.(*queryLease).client != nil {
			t.Fatal("reservation opened physical client")
		}
	}
	if _, err := s.ReserveQuery(33); !errors.Is(err, contract.ErrSQLSessionLimit) {
		t.Fatal("unbounded physical reservations")
	}
	canceled, stop := context.WithCancel(context.Background())
	stop()
	if err := leases[0].Preconnect(canceled); !errors.Is(err, context.Canceled) {
		t.Fatal("pre-cancel attempted invalid endpoint", err)
	}
	if leases[0].(*queryLease).client != nil || leases[0].(*queryLease).lost {
		t.Fatal("failed unpublished preconnect bound socket")
	}
	if _, err := leases[0].Execute(context.Background(), "bad keyspace", "SELECT id FROM rows"); !errors.Is(err, connections.ErrInvalid) {
		t.Fatal("invalid namespace reached connection", err)
	}
	if _, err := s.TableMetadata(context.Background(), contract.ObjectRef{ProfileID: 8, Catalog: "a", Kind: "table", Name: "rows"}); err == nil {
		t.Fatal("foreign object reached metadata socket")
	}
	for _, l := range leases {
		l.Release()
	}
	if len(s.leases) != 0 {
		t.Fatal("released reservations retained")
	}
	if _, err := leases[0].Execute(context.Background(), "", "SELECT id FROM rows"); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("released lease recovered implicitly")
	}
	s.revoke()
	if _, err := leases[0].Replace(); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("revoked original session replaced")
	}
}

func TestCassandraTypedJSONPreservesEmptyNestedMissingAndBinaryCopies(t *testing.T) {
	text := &Type{Code: 10}
	for _, code := range []uint16{0x20, 0x21, 0x22, 0x31, 0x30} {
		v := Value{Type: &Type{Code: code, Arguments: []*Type{text}}}
		cell, err := nativeCell(v)
		if err != nil || cell.Kind != contract.JSON {
			t.Fatal(err)
		}
		var raw map[string]json.RawMessage
		if json.Unmarshal([]byte(cell.Text), &raw) != nil {
			t.Fatal("typed JSON malformed")
		}
		field := "items"
		if code == 0x21 {
			field = "entries"
		}
		if code == 0x30 {
			field = "fields"
		}
		if string(raw[field]) != "[]" || raw["value"] != nil {
			t.Fatal("empty collection became empty scalar", cell.Text)
		}
	}
	udt := Value{Type: &Type{Code: 0x30, Keyspace: "Mixed", Name: "payload"}, Fields: []NamedValue{
		{"empty", Value{Type: text, Text: ""}}, {"null", Value{Type: text, Null: true}}, {"missing", Value{Type: text, Missing: true}},
	}}
	cell, err := nativeCell(udt)
	if err != nil || !strings.Contains(cell.Text, `"missing":true`) || !strings.Contains(cell.Text, `"null":true`) || !strings.Contains(cell.Text, `"value":""`) {
		t.Fatal("UDT states conflated", err)
	}
	leaf := Value{Type: text, Text: strings.Repeat("x", 1<<20), Raw: make([]byte, 1<<20)}
	nested := Value{Type: &Type{Code: 0x20, Arguments: []*Type{text}}, Items: []Value{leaf}, Raw: make([]byte, 1<<20)}
	nested = Value{Type: &Type{Code: 0x20, Arguments: []*Type{nested.Type}}, Items: []Value{nested}, Raw: make([]byte, 1<<20)}
	if _, err := nativeCell(nested); err != nil {
		t.Fatal("nonserialized parent bytes charged repeatedly", err)
	}
	bytes := []byte{0, 255}
	binaryCell, err := nativeCell(Value{Type: &Type{Code: 3}, Raw: bytes})
	if err != nil {
		t.Fatal(err)
	}
	bytes[0] = 99
	if binaryCell.Bytes[0] != 0 {
		t.Fatal("typed binary retained caller memory")
	}
	empty, _ := nativeCell(Value{Type: &Type{Code: 3}, Raw: []byte{}})
	if empty.Bytes == nil {
		t.Fatal("empty binary changed to nil")
	}
	if _, err := nativeCell(Value{Type: text, Missing: true}); !errors.Is(err, ErrProtocol) {
		t.Fatal("top-level unsupported missing value accepted")
	}
	large := contract.Cell{Kind: contract.Text, Text: strings.Repeat("猫", 400000)}
	preview := previewCell(large)
	if !preview.Truncated || len(preview.Text) > 1<<20 || strings.HasSuffix(preview.Text, "\xef\xbf\xbd") {
		t.Fatal("UTF-8 preview cut incorrectly")
	}
}

func TestCassandraKeyMetadataRejectsDuplicateOrIncompleteNativePositions(t *testing.T) {
	str := func(s string) Value { return Value{Type: &Type{Code: 10}, Text: s} }
	row := func(name, kind, pos, direction string) schemaRow {
		return schemaRow{
			"keyspace_name": str("Mixed"), "table_name": str("rows"), "column_name": str(name), "kind": str(kind), "type": str("int"), "clustering_order": str(direction), "position": {Type: &Type{Code: 9}, Text: pos},
		}
	}
	o := contract.ObjectRef{ProfileID: 7, Catalog: "Mixed", Kind: "table", Name: "rows"}
	for _, rows := range [][]schemaRow{
		{row("p", "partition_key", "1", "none")},
		{row("p", "partition_key", "0", "none"), row("q", "partition_key", "0", "none")},
		{row("p", "partition_key", "0", "none"), row("c", "clustering", "0", "none")},
		{row("p", "partition_key", "0", "none"), row("c", "clustering", "2", "asc")},
		{row("p", "partition_key", "0", "none"), row("r", "regular", "0", "none")},
	} {
		if _, _, err := tableColumns(rows, o); !errors.Is(err, ErrProtocol) {
			t.Fatal("unsafe native key accepted", err)
		}
	}
	cols, indexes, err := tableColumns([]schemaRow{row("c", "clustering", "0", "desc"), row("p", "partition_key", "0", "none"), row("s", "static", "-1", "none")}, o)
	if err != nil || cols[0].Name != "p" || cols[1].Name != "c" || indexes[0].Columns[1].Direction != "desc" || cols[0].Nullable || !cols[2].Nullable {
		t.Fatal("native key/order lost", err)
	}
	a, b := Identity{HostID: "original", SchemaVersion: "before"}, Identity{HostID: "original", SchemaVersion: "after"}
	if !sameNode(a, b) {
		t.Fatal("normal DDL rebound node identity")
	}
	b.HostID = "replacement"
	if sameNode(a, b) {
		t.Fatal("node replacement accepted")
	}
}

func TestCassandraConcurrentReleaseSettlesReservation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &Session{profile: connections.Profile{ID: 7}, ctx: ctx, cancel: cancel, leases: map[*queryLease]bool{}}
	l, err := s.ReserveQuery(1)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Go(l.Release)
	}
	wg.Wait()
	if len(s.leases) != 0 {
		t.Fatal("concurrent releases retained original lease")
	}
}
