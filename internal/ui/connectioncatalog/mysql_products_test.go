package connectioncatalog

import (
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestMySQLProductsAndEtcdHaveIndependentGridAndTreeEntries(t *testing.T) {
	for _, kind := range []connections.Kind{connections.TDSQL, connections.PolarDB, connections.GreatSQL, connections.Dolt, connections.Etcd} {
		s := New()
		descriptor, _ := connections.Describe(kind)
		s.search = descriptor.Label
		grid, tree := s.catalogItems("全部"), s.catalogTreeDirect()
		if len(grid) != 1 || len(tree) != 1 || grid[0].kind != kind || tree[0].kind != kind {
			t.Fatalf("%s must be discoverable in both layouts", kind)
		}
		var selected Choice
		s.chooseCatalogEntry(tree[0], func(choice Choice) { selected = choice })
		if selected.Kind != kind || selected.Name != descriptor.Label {
			t.Fatal("chooser changed independent product identity")
		}
		s.RecordRecent(selected.Name)
		if rows := s.catalogRecents(); len(rows) != 1 || rows[0].kind != kind {
			t.Fatal("recent selection changed independent product identity")
		}
		s.engineFilters = map[string]bool{"MySQL": true}
		if len(s.catalogItems("全部")) != 0 || len(s.catalogTreeDirect()) != 0 {
			t.Fatal("protocol family substituted for product filter")
		}
	}
}
