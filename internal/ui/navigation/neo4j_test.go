package navigation

import (
	"github.com/nyakang/nyadb/internal/services/connections"
	"iter"
	"slices"
	"testing"
)

func TestNeo4jNavigatorExplicitGraphDatabaseOpenAndNativeCategories(t *testing.T) {
	opened := false
	s := New(Dependencies{Engine: func(int64) connections.Kind { return connections.Neo4j }, SessionStatus: func(int64) string { return "已连接" }, DatabaseOpened: func(int64, string) bool { return opened }, Objects: func(int64, string) (iter.Seq[Object], bool) {
		return slices.Values([]Object{{Name: "Person", Kind: "图标签"}}), true
	}})
	db := DatabaseNode(7, "MixedCase")
	if len(s.Children(db.Key())) != 0 {
		t.Fatal("disclosure implicitly opened graph database")
	}
	opened = true
	children := s.Children(db.Key())
	expected := []string{"图标签", "关系类型", "图索引", "图约束", "图过程", "图函数", "查询"}
	if len(children) != len(expected) {
		t.Fatal("SQL categories in Neo4j graph database", children)
	}
	for i, key := range children {
		n := Parse(key)
		if n.Category != expected[i] || n.ProfileID != 7 || n.Database != "MixedCase" {
			t.Fatal("native navigation identity", n)
		}
	}
	types := s.Children(children[0])
	if len(types) != 1 || Parse(types[0]).Object != "Person" {
		t.Fatal("native type navigation lost")
	}
}
