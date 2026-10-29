package navigation

import (
	"iter"
	"slices"
	"testing"
)

func TestNavigatorCategoriesAndMetadataProgress(t *testing.T) {
	cache := map[string][]Object{}
	opened, pending, loadError := false, false, ""
	s := New(Dependencies{
		SessionStatus:  func(int64) string { return "已连接" },
		DatabaseOpened: func(int64, string) bool { return opened },
		Objects: func(_ int64, db string) (iter.Seq[Object], bool) {
			objects, ok := cache[db]
			return slices.Values(objects), ok
		},
		LoadState: func(int64, string) (bool, string) { return pending, loadError },
	})
	db := DatabaseNode(int64(1), "app_db")

	checkStatus := func(want string) {
		t.Helper()
		children := s.Children(db.Key())
		if len(children) != 1 {
			t.Fatalf("expected one status row, got %v", children)
		}
		n := Parse(children[0])
		if n.Kind != "status" || n.Object != want || n.Database != "app_db" {
			t.Fatalf("unexpected loading node: %+v", n)
		}
	}
	if children := s.Children(db.Key()); children != nil {
		t.Fatalf("unopened database has disclosure children: %v", children)
	}
	opened = true
	checkStatus("waiting")
	pending = true
	checkStatus("loading")
	pending = false
	loadError = "permission denied"
	checkStatus("error")

	cache["app_db"] = []Object{
		{Name: "customers", Kind: "表"},
		{Name: "customers", Kind: "视图"},
		{Name: "get_customer", Kind: "函数"},
		{Name: "refresh_customer", Kind: "存储过程"},
		{Name: "nightly_cleanup", Kind: "事件"},
	}
	children := s.Children(db.Key())
	if len(children) != 7 {
		t.Fatalf("incomplete database categories: got %d", len(children))
	}
	for i, key := range children {
		n := Parse(key)
		if n.Kind != "category" || n.Category != []string{"表", "视图", "函数", "存储过程", "事件", "查询", "备份"}[i] {
			t.Fatalf("incorrect category order: %+v", n)
		}
		items := s.Children(key)
		if SupportedCategory(n.Category) {
			if len(items) != 1 || Parse(items[0]).Category != n.Category ||
				Parse(items[0]).Kind != "object" {
				t.Fatalf("supported category %q lost real objects: %v", n.Category, items)
			}
		} else if len(items) != 0 {
			t.Fatalf("unsupported category %q fabricated objects: %v", n.Category, items)
		}
	}
}
