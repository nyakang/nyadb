package navigation

import (
	"iter"
	"slices"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestDamengCatalogAndNativeProgramsKeepIndependentIdentity(t *testing.T) {
	s := New(Dependencies{Engine: func(int64) connections.Kind { return connections.Dameng }, Catalog: func(int64) string { return "DAMENG" }, SessionStatus: func(int64) string { return "已连接" }, Databases: func(int64) []string { return []string{` Schema " `} }, DatabaseOpened: func(int64, string) bool { return true }, Objects: func(int64, string) (iter.Seq[Object], bool) {
		return slices.Values([]Object{{Name: "package", Kind: "包"}, {Name: "sequence", Kind: "序列"}}), true
	}})
	children := s.Children(ConnectionKey(7))
	if len(children) == 0 || Parse(children[0]).Kind != "catalog" || Parse(children[0]).Catalog != "DAMENG" {
		t.Fatal("native catalog omitted")
	}
	children = s.Children(children[0])
	if len(children) != 1 || Parse(children[0]).Database != ` Schema " ` {
		t.Fatal("native schema identity was normalized as a label")
	}
	for _, key := range s.Children(children[0]) {
		if category := Parse(key).Category; category == "包" || category == "序列" {
			if nodes := s.Children(key); len(nodes) != 1 {
				t.Fatal("native program omitted", category)
			}
		}
	}
	s.Filter.Engine = connections.Dameng
	if !s.ProfileVisible(connections.Profile{Name: "DM8", Kind: connections.Dameng}) || s.ProfileVisible(connections.Profile{Name: "DM8", Kind: connections.Oracle}) {
		t.Fatal("native product filter merged Oracle identity")
	}
}
