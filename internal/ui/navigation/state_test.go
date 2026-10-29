package navigation

import (
	"reflect"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestNavigatorFiltersPreserveGroupOrderAndMembership(t *testing.T) {
	s := New(Dependencies{})
	s.SetProfiles([]connections.Profile{
		{ID: 1, Name: "Alpha", Kind: connections.MySQL, GroupName: "Development", Starred: true},
		{ID: 2, Name: "Beta", Kind: connections.SQLite, GroupName: "development"},
		{ID: 3, Name: "Standalone", Kind: connections.PostgreSQL},
	}, []connections.Group{{Name: "Empty"}, {Name: "Development"}}, 0)
	for _, tc := range []struct {
		name            string
		filter          Filter
		roots, children []string
	}{
		{name: "all", roots: []string{GroupKey("Empty"), GroupKey("Development"), ConnectionKey(3)}, children: []string{ConnectionKey(1), ConnectionKey(2)}},
		{name: "trimmed case insensitive group search", filter: Filter{Search: " DEV "}, roots: []string{GroupKey("Development")}, children: []string{ConnectionKey(1), ConnectionKey(2)}},
		{name: "profile search", filter: Filter{Search: "alpha"}, roots: []string{GroupKey("Development")}, children: []string{ConnectionKey(1)}},
		{name: "combined filters", filter: Filter{Engine: connections.MySQL, StarredOnly: true}, roots: []string{GroupKey("Development")}, children: []string{ConnectionKey(1)}},
		{name: "root engine", filter: Filter{Engine: connections.PostgreSQL}, roots: []string{ConnectionKey(3)}, children: []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s.Filter = tc.filter
			if got := s.Roots(); !reflect.DeepEqual(got, tc.roots) {
				t.Fatalf("roots = %v, want %v", got, tc.roots)
			}
			if got := s.Children(GroupKey("Development")); !reflect.DeepEqual(got, tc.children) {
				t.Fatalf("children = %v, want %v", got, tc.children)
			}
			if s.GroupCount("DEVELOPMENT") != 2 {
				t.Fatal("filters changed persisted group membership")
			}
		})
	}
}

func TestNavigatorTemporaryRevealKeepsOriginalFiltersUntilManualChange(t *testing.T) {
	s := New(Dependencies{})
	original := Filter{Search: "hidden", Engine: connections.SQLite, StarredOnly: true}
	s.Filter = original
	s.Tree.Open.Add(GroupKey("Development"))
	s.SuspendFilter()
	s.SuspendFilter() // A second document reveal must not overwrite the first snapshot.
	if s.Filter != (Filter{}) || !s.CanRestoreFilter() {
		t.Fatal("temporary reveal lost filter snapshot")
	}
	s.RestoreFilter()
	if s.Filter != original || s.CanRestoreFilter() {
		t.Fatal("original filters were not restored")
	}
	s.SuspendFilter()
	s.Filter.Search = "manual search"
	s.ChangeFilter()
	s.RestoreFilter()
	if s.Filter.Search != "manual search" || s.CanRestoreFilter() {
		t.Fatal("manual filters were overwritten by a stale snapshot")
	}
	if !s.Tree.Open.Has(GroupKey("Development")) {
		t.Fatal("filter changes discarded disclosure state")
	}
}

func TestNavigatorRefreshKeepsQualifiedSelectionAcrossRename(t *testing.T) {
	s := New(Dependencies{SessionStatus: func(id int64) string {
		if id == 1 {
			return "已连接"
		}
		return "未连接"
	}})
	node := Node{Kind: "object", ProfileID: 1, Database: "db", Category: "表", Object: "same_table"}
	s.Key = node.Key()
	profiles := []connections.Profile{{ID: 1, Name: "Renamed", GroupName: "Group", Kind: connections.MySQL}}
	if !s.SetProfiles(profiles, []connections.Group{{Name: "Group"}}, 1) || s.Key != node.Key() || !s.Tree.Open.Has(GroupKey("Group")) {
		t.Fatal("refresh lost selected object's identity or group disclosure")
	}
	s.Tree.Open.Add(ConnectionKey(1))
	s.Key = ConnectionKey(1)
	if got := s.SelectionRow(s.Roots()); got != 1 {
		t.Fatalf("first frame selected row = %d, want 1", got)
	}
	s.Group = "Removed"
	if s.SetProfiles(nil, nil, 1) || s.Group != "" || s.Key != "我的连接" {
		t.Fatal("deleted profile or group remained selected")
	}
}

func TestNavigatorNodeIdentityQualifiesProfileDatabaseAndCategory(t *testing.T) {
	node := Node{Kind: "object", ProfileID: 1, Database: "db:\"数据库", Category: "表", Object: "same_table"}
	if got := Parse(node.Key()); got != node {
		t.Fatalf("node did not round trip: %+v", got)
	}
	for _, other := range []Node{
		{Kind: node.Kind, ProfileID: 2, Database: node.Database, Category: node.Category, Object: node.Object},
		{Kind: node.Kind, ProfileID: 1, Database: "other", Category: node.Category, Object: node.Object},
		{Kind: node.Kind, ProfileID: 1, Database: node.Database, Category: "视图", Object: node.Object},
	} {
		if node.Key() == other.Key() {
			t.Fatal("different objects share a tree identity")
		}
	}
	for _, invalid := range []string{"Local", GroupKey("Group"), navigationPrefix + "{"} {
		if Parse(invalid) != (Node{}) {
			t.Fatalf("invalid key parsed as a database node: %q", invalid)
		}
	}
}
