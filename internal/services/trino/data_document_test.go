package trino

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestDataDocumentReservationAndClosePerformNoNetworkWork(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &Session{ctx: ctx, cancel: cancel, profile: connections.Profile{ID: 39, Kind: connections.Trino, Host: "unreachable.invalid"}}
	o := contract.NamespaceRef{ProfileID: 39, Catalog: "original", Schema: "schema"}.Object("TABLE", "items")
	l, err := s.ReserveDataDocument(o)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.dataLeases) != 1 {
		t.Fatal("original reservation missing")
	}
	r := l.Runtime()
	if !r.SequentialWrites || r.Chunk != nil || r.RowReader != nil {
		t.Fatal("unverified native identity or field reader advertised")
	}
	if err = l.Close(); err != nil {
		t.Fatal(err)
	}
	if err = l.Close(); err != nil || len(s.dataLeases) != 0 {
		t.Fatal("close failed to unregister original lease")
	}
	if _, err = r.AdvancedLoader(context.Background(), 39, namespaceLabel(o.Catalog, o.Schema), o.Name, contract.DataOptions{Object: o, Limit: 1}); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("closed document admitted work", err)
	}
}

func TestCompleteBinaryInsertRetainsItsExpandedNativeSQL(t *testing.T) {
	o := contract.NamespaceRef{ProfileID: 39, Catalog: "original", Schema: "schema"}.Object("TABLE", "items")
	m := &contract.TableStructure{Object: o, Kind: "TABLE", Columns: []contract.StructureColumn{{Name: "payload", Type: "varbinary", ValueKind: contract.Binary}}}
	value := contract.DataValue{Kind: contract.Binary, Bytes: make([]byte, contract.DataEditMaxBytes)}
	value.Bytes[len(value.Bytes)-1] = 255
	q, err := insertStatement(m, map[string]contract.DataValue{"payload": value})
	if err != nil || len(q) <= contract.DataEditMaxBytes || !strings.Contains(q, "ff' AS varbinary)") {
		t.Fatal("complete binary was narrowed by its SQL encoding", err)
	}
	value.Bytes = append(value.Bytes, 0)
	if _, err = insertStatement(m, map[string]contract.DataValue{"payload": value}); !errors.Is(err, contract.ErrDataEditLimit) {
		t.Fatal("complete editor byte limit changed", err)
	}
}
