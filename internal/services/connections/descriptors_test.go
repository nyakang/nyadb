package connections_test

import (
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"testing"
)

func TestDescriptorsAreIndependentImmutableValues(t *testing.T) {
	for _, kind := range []connections.Kind{connections.MySQL, connections.PostgreSQL, connections.SQLite} {
		d, ok := connections.Describe(kind)
		if !ok || d.Kind != kind || d.ClientFamily == "" || d.Dialect == "" {
			t.Fatal("missing engine identity")
		}
		before := d
		d.Capabilities.Query = false
		d.Label = "changed"
		after, _ := connections.Describe(kind)
		if after != before {
			t.Fatal("descriptor mutation leaked")
		}
		if !after.Capabilities.Count || !after.Capabilities.FieldChunks || !after.Capabilities.Keyset || !after.Capabilities.Analysis {
			t.Fatal("implemented data features are disabled")
		}
		if kind != connections.MySQL && (after.Capabilities.Import || after.Capabilities.Backup || kind != connections.PostgreSQL && after.Capabilities.DesignTables) {
			t.Fatal("unsupported feature enabled")
		}
	}
	d, ok := connections.Describe(connections.Kind("unknown"))
	if ok || d.Capabilities != (contract.Capabilities{}) {
		t.Fatal("unknown capabilities must default off")
	}
}
