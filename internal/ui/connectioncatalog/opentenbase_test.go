package connectioncatalog

import (
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestOpenTenBaseChoiceRetainsProductThroughBothLayoutsAndRecents(t *testing.T) {
	s := New()
	s.search = "OpenTenBase"
	for _, rows := range [][]catalogEntry{s.catalogItems("全部"), s.catalogTreeDirect()} {
		if len(rows) != 1 || rows[0].kind != connections.OpenTenBase {
			t.Fatal("native product missing from chooser")
		}
		s.chooseCatalogEntry(rows[0], func(choice Choice) {
			if choice.Kind != connections.OpenTenBase || choice.Name != "OpenTenBase" {
				t.Fatal("chooser changed native identity")
			}
		})
		s.RecordRecent(rows[0].name)
	}
	if rows := s.catalogRecents(); len(rows) != 1 || rows[0].kind != connections.OpenTenBase {
		t.Fatal("recent native product identity changed")
	}
	s.engineFilters = map[string]bool{"PostgreSQL": true}
	if len(s.catalogItems("全部")) != 0 || len(s.catalogTreeDirect()) != 0 {
		t.Fatal("native product merged into protocol filter")
	}
}
