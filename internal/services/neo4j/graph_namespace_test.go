package neo4j

import (
	"context"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"testing"
)

func TestGraphNamespaceReservationIsQualifiedLazyAndOnlyGraphTransfer(t *testing.T) {
	s := fakeSessionTest(t)
	s.openClient = func(context.Context, connections.Neo4jConfig, string) (*Client, error) {
		t.Fatal("namespace reservation performed I/O")
		return nil, nil
	}
	l := dataLeaseTest(t, s, contract.GraphNamespaceKind, "")
	r := l.Runtime()
	c := contract.BoundDataCapabilities(r)
	if !c.GraphExchange || !c.Import || !c.Export || !c.Documents || c.EditableTables || c.Keyset || c.Count || c.FieldChunks || r.Loader != nil || r.AdvancedLoader != nil || r.Saver != nil || r.Importer != nil || r.JSONL != nil || r.ReadGraphEndpoints != nil {
		t.Fatal("namespace fabricated grid semantics", c)
	}
	for _, o := range []contract.ObjectRef{s.Namespace("system").Object(contract.GraphNamespaceKind, ""), s.Namespace("neo4j").Object(contract.GraphNamespaceKind, "invented"), {ProfileID: 99, Catalog: "neo4j", Kind: contract.GraphNamespaceKind}, {ProfileID: 42, Catalog: "neo4j", Schema: "fabricated", Kind: contract.GraphNamespaceKind}} {
		if p, e := s.ReserveDataDocument(o); e == nil {
			p.Close()
			t.Fatal("foreign, system or named namespace admitted", o)
		}
	}
}
