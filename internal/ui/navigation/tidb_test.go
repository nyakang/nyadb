package navigation

import (
	"iter"
	"slices"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestTiDBNavigatorKeepsSequenceAndProductIdentity(t *testing.T) {
	s := New(Dependencies{Engine: func(int64) connections.Kind { return connections.TiDB }, SessionStatus: func(int64) string { return "已连接" }, DatabaseOpened: func(int64, string) bool { return true }, Objects: func(int64, string) (iter.Seq[Object], bool) {
		return slices.Values([]Object{{Name: "seq", Kind: "序列"}}), true
	}})
	children := s.Children(DatabaseNode(7, "app").Key())
	if len(children) != 4 {
		t.Fatal("incorrect TiDB categories", children)
	}
	for i, key := range children {
		if Parse(key).Category != []string{"表", "视图", "序列", "查询"}[i] {
			t.Fatal("incorrect category", key)
		}
	}
	seq := s.Children(children[2])
	if len(seq) != 1 || Parse(seq[0]).Object != "seq" {
		t.Fatal("sequence navigation missing")
	}
}
