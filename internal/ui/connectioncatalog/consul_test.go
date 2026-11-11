package connectioncatalog

import (
	"github.com/nyakang/nyadb/internal/services/connections"
	"testing"
)

func TestConsulChooserSearchFiltersSelectionAndRecents(t *testing.T) {
	s := New()
	s.search = "consul"
	items := s.catalogItems("全部")
	if len(items) != 1 || items[0].kind != connections.Consul || items[0].engine != "Consul" {
		t.Fatal("native chooser identity", items)
	}
	var chosen Choice
	s.chooseCatalogEntry(items[0], func(c Choice) { chosen = c })
	if chosen.Kind != connections.Consul || s.Selected() != chosen {
		t.Fatal("selection lost native identity")
	}
	if len(s.catalogRecents()) != 0 {
		t.Fatal("selection alone created recent")
	}
	s.RecordRecent(chosen.Name)
	if recent := s.catalogRecents(); len(recent) != 1 || recent[0].kind != connections.Consul {
		t.Fatal("native recent identity")
	}
	s.engineFilters = map[string]bool{"PostgreSQL": true}
	if len(s.catalogTreeDirect()) != 0 {
		t.Fatal("native product matched SQL family")
	}
	s.engineFilters = map[string]bool{"Consul": true}
	if rows := s.catalogTreeDirect(); len(rows) != 1 || rows[0].kind != connections.Consul {
		t.Fatal("tree filter", rows)
	}
}
