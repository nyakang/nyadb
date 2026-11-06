package navigation

import (
	"github.com/nyakang/nyadb/internal/services/connections"
	"iter"
	"slices"
	"testing"
)

func TestCockroachQualifiedNavigatorAndIndependentFilter(t *testing.T) {
	s := New(Dependencies{Engine: func(int64) connections.Kind { return connections.CockroachDB }, SessionStatus: func(int64) string { return "已连接" }, DatabaseOpened: func(int64, string) bool { return true }, Objects: func(int64, string) (iter.Seq[Object], bool) {
		return slices.Values([]Object{{Name: "seq", Kind: "序列"}}), true
	}})
	children := s.Children(DatabaseNode(7, "public").Key())
	sequence := ""
	for _, key := range children {
		if Parse(key).Category == "序列" {
			sequence = key
		}
	}
	nodes := s.Children(sequence)
	if sequence == "" || len(nodes) != 1 || Parse(nodes[0]).Object != "seq" {
		t.Fatal("sequence/schema navigation missing")
	}
	s.Filter.Engine = connections.CockroachDB
	if !s.ProfileVisible(connections.Profile{Name: "cluster", Kind: connections.CockroachDB}) || s.ProfileVisible(connections.Profile{Name: "cluster", Kind: connections.PostgreSQL}) {
		t.Fatal("product filter merged PostgreSQL")
	}
}
