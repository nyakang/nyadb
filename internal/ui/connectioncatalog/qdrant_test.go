package connectioncatalog

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"testing"
)

func TestQdrantChooserSearchSelectionAndIndependentFilters(t *testing.T) {
	s := New()
	s.search = "qdrant"
	rows := s.catalogItems("全部")
	if len(rows) != 1 || rows[0].kind != connections.Qdrant || rows[0].engine != "Qdrant" {
		t.Fatal(rows)
	}
	tt := ui.NewTester(catalogTestView(s), 720, 520)
	if err := tt.Click("选择 Qdrant"); err != nil {
		t.Fatal(err)
	}
	if s.Selected().Kind != connections.Qdrant {
		t.Fatal(s.Selected())
	}
	s.engineFilters = map[string]bool{"Elasticsearch": true}
	if len(s.catalogItems("全部")) != 0 {
		t.Fatal("Qdrant inherited another product's filter")
	}
	s.engineFilters = map[string]bool{"Qdrant": true}
	if len(s.catalogTreeDirect()) != 1 {
		t.Fatal("Qdrant engine filter lost selection")
	}
}
