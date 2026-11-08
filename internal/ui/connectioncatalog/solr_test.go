package connectioncatalog

import (
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestSolrChooserSearchAndFiltersKeepIndependentProduct(t *testing.T) {
	s := New()
	s.search = "solr"
	items := s.catalogItems("全部")
	if len(items) != 1 || items[0].kind != connections.Solr || items[0].engine != "Apache Solr" {
		t.Fatal("native Solr chooser entry", items)
	}
	var chosen Choice
	s.chooseCatalogEntry(items[0], func(c Choice) { chosen = c })
	if chosen.Kind != connections.Solr || s.Selected() != chosen {
		t.Fatal("Solr selection lost native identity")
	}
	setCatalogFilter(s.engineFilters, "Elasticsearch", true)
	if len(s.catalogTreeDirect()) != 0 {
		t.Fatal("Solr matched another REST product's filter")
	}
	s.engineFilters = map[string]bool{"Apache Solr": true}
	if rows := s.catalogTreeDirect(); len(rows) != 1 || rows[0].kind != connections.Solr {
		t.Fatal("Solr tree filter", rows)
	}
}
