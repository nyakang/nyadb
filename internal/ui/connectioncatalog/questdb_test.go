package connectioncatalog

import (
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestQuestDBProductSelectionSurvivesGridTreeFiltersAndRecents(t *testing.T) {
	s := New()
	s.search = "QuestDB"
	s.engineFilters = map[string]bool{"QuestDB": true}
	grid, tree := s.catalogItems("全部"), s.catalogTreeDirect()
	if len(grid) != 1 || len(tree) != 1 || grid[0].kind != connections.QuestDB || tree[0].kind != connections.QuestDB {
		t.Fatal("native product missing from chooser layouts")
	}
	var selected Choice
	s.chooseCatalogEntry(tree[0], func(c Choice) { selected = c })
	s.RecordRecent(selected.Name)
	if selected.Kind != connections.QuestDB || len(s.catalogRecents()) != 1 {
		t.Fatal("native product identity lost")
	}
	s.engineFilters = map[string]bool{"PostgreSQL": true}
	if len(s.catalogItems("全部")) != 0 || len(s.catalogTreeDirect()) != 0 {
		t.Fatal("transport family substituted for product identity")
	}
}
