package navigation

import (
	"github.com/nyakang/nyadb/internal/services/connections"
	"iter"
	"slices"
	"testing"
)

func TestCassandraNavigatorExplicitKeyspaceOpenAndNativeCategories(t *testing.T) {
	opened := false
	s := New(Dependencies{Engine: func(int64) connections.Kind { return connections.Cassandra }, SessionStatus: func(int64) string { return "已连接" }, DatabaseOpened: func(int64, string) bool { return opened }, Objects: func(int64, string) (iter.Seq[Object], bool) {
		return slices.Values([]Object{{Name: "address", Kind: "类型"}}), true
	}})
	db := DatabaseNode(7, "MixedCase")
	if len(s.Children(db.Key())) != 0 {
		t.Fatal("disclosure implicitly opened keyspace")
	}
	opened = true
	children := s.Children(db.Key())
	expected := []string{"表", "视图", "类型", "查询"}
	if len(children) != len(expected) {
		t.Fatal("SQL categories in Cassandra keyspace", children)
	}
	for i, key := range children {
		n := Parse(key)
		if n.Category != expected[i] || n.ProfileID != 7 || n.Database != "MixedCase" {
			t.Fatal("native navigation identity", n)
		}
	}
	types := s.Children(children[2])
	if len(types) != 1 || Parse(types[0]).Object != "address" {
		t.Fatal("native type navigation lost")
	}
}
