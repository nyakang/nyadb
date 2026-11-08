package navigation

import (
	"github.com/nyakang/nyadb/internal/services/connections"
	"iter"
	"slices"
	"testing"
)

func TestCouchDBNavigatorQualifiedDatabaseInfoAndDesignCategories(t *testing.T) {
	s := New(Dependencies{Engine: func(int64) connections.Kind { return connections.CouchDB }, SessionStatus: func(int64) string { return "已连接" }, DatabaseOpened: func(int64, string) bool { return true }, Objects: func(_ int64, db string) (iter.Seq[Object], bool) {
		return slices.Values([]Object{{Name: db, Kind: "数据库信息"}, {Name: "_design/shared", Kind: "设计文档"}}), true
	}})
	for _, db := range []string{"owned", "other"} {
		categories := s.Children(DatabaseNode(7, db).Key())
		if len(categories) != 3 {
			t.Fatal(categories)
		}
		for i, want := range []string{"数据库信息", "设计文档", "查询"} {
			if Parse(categories[i]).Category != want || !SupportedCategory(want) {
				t.Fatal(categories)
			}
		}
		children := s.Children(categories[1])
		if len(children) != 1 {
			t.Fatal(children)
		}
		n := Parse(children[0])
		if n.ProfileID != 7 || n.Database != db || n.Object != "_design/shared" || n.Category != "设计文档" {
			t.Fatal("qualified native object lost", n)
		}
	}
}
