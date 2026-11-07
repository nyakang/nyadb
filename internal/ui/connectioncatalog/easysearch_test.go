package connectioncatalog

import (
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestEasysearchChooserSearchFiltersAndSelectionKeepIndependentIdentity(t *testing.T) {
	s := New()
	s.search = "easysearch"
	items := s.catalogItems("全部")
	if len(items) != 1 || items[0].kind != connections.Easysearch || items[0].engine != "Easysearch" {
		t.Fatal("search did not resolve the native public product")
	}
	chosen := Choice{}
	s.chooseCatalogEntry(items[0], func(c Choice) { chosen = c })
	if s.Selected().Kind != connections.Easysearch || chosen.Kind != connections.Easysearch {
		t.Fatal("chooser did not deliver the native editor selection")
	}
	setCatalogFilter(s.engineFilters, "Elasticsearch", true)
	if len(s.catalogItems("全部")) != 0 || len(s.catalogTreeDirect()) != 0 {
		t.Fatal("Easysearch matched another product's engine filter")
	}
	s.clearCatalogFilters()
	setCatalogFilter(s.engineFilters, "Easysearch", true)
	if rows := s.catalogTreeDirect(); len(rows) != 1 || rows[0].kind != connections.Easysearch {
		t.Fatal("native tree filter lost public product identity")
	}
}
