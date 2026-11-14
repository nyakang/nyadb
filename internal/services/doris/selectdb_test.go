package doris

import (
	"errors"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestSelectDBSQLRetainsProductSemanticsAndOriginalCatalog(t *testing.T) {
	// These statements must be dispatched to SelectDB unchanged. They must
	// not inherit Doris 4.1.3's opaque-state or transaction preflight.
	for _, source := range []string{"BEGIN; SELECT 1; COMMIT", "SET return_object_data_as_binary=false; SELECT 1", "SET autocommit=0", "SWITCH other"} {
		parts, err := productStatements(source, true)
		if err != nil || len(parts) == 0 {
			t.Fatalf("SelectDB inherited Doris-specific rejection: %s", source)
		}
		if _, err = statements(source); !errors.Is(err, contract.ErrUnsupported) {
			t.Fatalf("Doris native preflight changed: %s", source)
		}
	}
	for _, source := range []string{"SET sql_mode='ANSI_QUOTES'", "/*! SET sql_mode='NO_BACKSLASH_ESCAPES' */", "SELECT 'unterminated"} {
		if _, err := productStatements(source, true); err == nil {
			t.Fatal("SelectDB admitted incompatible literal rules")
		}
	}
	p, _ := connections.Defaults(connections.SelectDB)
	p.ID, p.Name, p.SelectDBOptions.Catalog = 71, "original", "lake`quoted"
	s := &Session{profile: p}
	n := s.Namespace("warehouse")
	if n != (contract.NamespaceRef{ProfileID: p.ID, Catalog: "lake`quoted", Schema: "warehouse"}) {
		t.Fatal("SelectDB catalog collapsed into a navigation label")
	}
	if err := s.checkNamespace(n); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []contract.NamespaceRef{{ProfileID: 72, Catalog: n.Catalog, Schema: n.Schema}, {ProfileID: 71, Catalog: "internal", Schema: n.Schema}} {
		if err := s.checkNamespace(bad); !errors.Is(err, connections.ErrInvalid) {
			t.Fatal("SelectDB read accepted another profile or catalog")
		}
	}
	for _, comment := range []string{"MySQL Community Server", "doris version doris-4.1.3-test", "StarRocks"} {
		if err := verifySelectDBIdentity(comment, "internal"); err == nil {
			t.Fatal("SelectDB accepted a substitute product")
		}
	}
}
