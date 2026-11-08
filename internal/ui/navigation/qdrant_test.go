package navigation

import (
	"github.com/nyakang/nyadb/internal/services/connections"
	"iter"
	"slices"
	"testing"
)

func TestQdrantNavigatorQualifiedCollectionCategories(t *testing.T) {
	s := New(Dependencies{Engine: func(int64) connections.Kind { return connections.Qdrant }, SessionStatus: func(int64) string { return "已连接" }, DatabaseOpened: func(int64, string) bool { return true }, Objects: func(_ int64, index string) (iter.Seq[Object], bool) {
		return slices.Values([]Object{{Name: index, Kind: "集合信息"}, {Name: "points", Kind: "向量点"}, {Name: "aliases", Kind: "集合别名"}}), true
	}})
	for _, index := range []string{"owned", "other"} {
		categories := s.Children(DatabaseNode(7, index).Key())
		if len(categories) != 4 {
			t.Fatal(categories)
		}
		for i, want := range []string{"集合信息", "向量点", "集合别名", "查询"} {
			if Parse(categories[i]).Category != want || !SupportedCategory(want) {
				t.Fatal(categories)
			}
		}
		children := s.Children(categories[2])
		if len(children) != 1 {
			t.Fatal(children)
		}
		n := Parse(children[0])
		if n.ProfileID != 7 || n.Database != index || n.Object != "aliases" || n.Category != "集合别名" {
			t.Fatal(n)
		}
	}
}
