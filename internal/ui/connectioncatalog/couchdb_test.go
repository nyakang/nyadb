package connectioncatalog

import (
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestCouchDBChooserSearchAndFiltersKeepIndependentProduct(t *testing.T) {
	s := New()
	s.search = "couchdb"
	items := s.catalogItems("全部")
	if len(items) != 1 || items[0].kind != connections.CouchDB || items[0].engine != "Apache CouchDB" {
		t.Fatal("native CouchDB chooser entry", items)
	}
	var chosen Choice
	s.chooseCatalogEntry(items[0], func(c Choice) { chosen = c })
	if chosen.Kind != connections.CouchDB || s.Selected() != chosen {
		t.Fatal("CouchDB selection lost native identity")
	}
	setCatalogFilter(s.engineFilters, "Elasticsearch", true)
	if len(s.catalogTreeDirect()) != 0 {
		t.Fatal("CouchDB matched another REST product's filter")
	}
	s.engineFilters = map[string]bool{"Apache CouchDB": true}
	if rows := s.catalogTreeDirect(); len(rows) != 1 || rows[0].kind != connections.CouchDB {
		t.Fatal("CouchDB tree filter", rows)
	}
}
