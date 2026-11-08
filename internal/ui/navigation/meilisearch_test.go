package navigation

import (
	"github.com/nyakang/nyadb/internal/services/connections"
	"iter"
	"slices"
	"testing"
)

func TestMeilisearchNavigatorQualifiedIndexCategories(t *testing.T) {
	s := New(Dependencies{Engine: func(int64) connections.Kind { return connections.Meilisearch }, SessionStatus: func(int64) string { return "已连接" }, DatabaseOpened: func(int64, string) bool { return true }, Objects: func(_ int64, index string) (iter.Seq[Object], bool) {
		return slices.Values([]Object{{Name: index, Kind: "索引信息"}, {Name: "documents", Kind: "文档"}, {Name: "settings", Kind: "索引设置"}, {Name: "stats", Kind: "索引统计"}, {Name: "tasks", Kind: "任务"}}), true
	}})
	for _, index := range []string{"owned", "other"} {
		categories := s.Children(DatabaseNode(7, index).Key())
		if len(categories) != 6 {
			t.Fatal(categories)
		}
		for i, want := range []string{"索引信息", "文档", "索引设置", "索引统计", "任务", "查询"} {
			if Parse(categories[i]).Category != want || !SupportedCategory(want) {
				t.Fatal(categories)
			}
		}
		children := s.Children(categories[2])
		if len(children) != 1 {
			t.Fatal(children)
		}
		n := Parse(children[0])
		if n.ProfileID != 7 || n.Database != index || n.Object != "settings" || n.Category != "索引设置" {
			t.Fatal(n)
		}
	}
}
