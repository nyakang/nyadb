package navigation

import "testing"

func TestOfflineQueryGroupsDoNotReadDatabaseMetadata(t *testing.T) {
	reads := 0
	s := New(Dependencies{SessionStatus: func(int64) string { return "未连接" }, QueryGroups: func(int64) []Object { return []Object{{Name: "Reports", Kind: "g"}} }, Queries: func(int64, string) []Object {
		return []Object{{Name: "Grouped", Kind: "q", Group: "g"}, {Name: "Loose", Kind: "loose"}}
	}, Databases: func(int64) []string { reads++; return nil }})
	roots := s.Children(ConnectionKey(7))
	if len(roots) != 1 || Parse(roots[0]).Category != "查询" {
		t.Fatalf("offline queries unavailable: %v", roots)
	}
	children := s.Children(roots[0])
	if len(children) != 2 || Parse(children[0]).Kind != "query_group" || Parse(children[1]).Object != "loose" {
		t.Fatalf("group tree: %v", children)
	}
	grouped := s.Children(children[0])
	if len(grouped) != 1 || Parse(grouped[0]).Object != "q" || reads != 0 {
		t.Fatal("query browsing started metadata or lost group")
	}
}
