package connectioncatalog

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"testing"
)

func TestMeilisearchChooserSearchSelectionAndIndependentFilters(t *testing.T) {
	s := New()
	s.search = "meilisearch"
	rows := s.catalogItems("全部")
	if len(rows) != 1 || rows[0].kind != connections.Meilisearch || rows[0].engine != "Meilisearch" {
		t.Fatal(rows)
	}
	tt := ui.NewTester(catalogTestView(s), 720, 520)
	if err := tt.Click("选择 Meilisearch"); err != nil {
		t.Fatal(err)
	}
	if s.Selected().Kind != connections.Meilisearch {
		t.Fatal(s.Selected())
	}
	s.engineFilters = map[string]bool{"Elasticsearch": true}
	if len(s.catalogItems("全部")) != 0 {
		t.Fatal("Meilisearch inherited another product's filter")
	}
	s.engineFilters = map[string]bool{"Meilisearch": true}
	if len(s.catalogTreeDirect()) != 1 {
		t.Fatal("Meilisearch engine filter lost selection")
	}
}
