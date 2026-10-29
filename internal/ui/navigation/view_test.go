package navigation

import (
	"iter"
	"slices"

	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/storage/localdb"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func TestUnifiedNavigatorVirtualizesMoreThanOneHundredObjects(t *testing.T) {
	s, tt, _, _ := navigationTestState(t)
	items := make([]Object, 1500)
	for i := range items {
		items[i] = Object{Name: fmt.Sprintf("table_%04d", i), Kind: "表"}
	}
	s.deps.SessionStatus = func(int64) string { return "已连接" }
	s.deps.Databases = func(int64) []string { return []string{"app_db"} }
	s.deps.DatabaseOpened = func(int64, string) bool { return true }
	s.deps.Objects = func(int64, string) (iter.Seq[Object], bool) { return slices.Values(items), true }
	s.SetProfiles([]connections.Profile{{ID: 1, Name: "Local", Kind: connections.MySQL}}, nil, 1)
	n := Node{Kind: "category", ProfileID: int64(1), Database: "app_db", Category: "表"}
	s.Tree.Open.Add(ConnectionKey(1))
	s.Tree.Open.Add(DatabaseNode(1, "app_db").Key())
	s.Key, s.Row = n.Key(), -1
	s.Tree.Open.Add(n.Key())
	tt.Frame()
	if s.Tree.Rows() < 1500 {
		t.Fatalf("tree truncated to %d nodes", s.Tree.Rows())
	}
	rendered := 0
	for _, text := range tt.Texts() {
		if strings.HasPrefix(text, "table_") {
			rendered++
		}
	}
	if rendered >= 200 {
		t.Fatalf("tree eagerly rendered %d objects", rendered)
	}
}

func TestNavigatorGroupRenameEditsInsideOriginalTreeRow(t *testing.T) {
	s, tt, svc, refresh := navigationTestState(t)
	ctx := context.Background()
	if err := svc.CreateGroup(ctx, "OldGroup"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Create(ctx, connections.Profile{Name: "Sibling", Kind: connections.MySQL, Host: "localhost", Port: 3306}); err != nil {
		t.Fatal(err)
	}
	refresh()
	tt.Frame()
	before, ok := tt.Find("Sibling")
	if !ok {
		t.Fatal("missing sibling")
	}
	if err := tt.RightClick("OldGroup"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("重命名"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !s.GroupEdit.Active || s.GroupEdit.Original != "OldGroup" {
		t.Fatal("rename did not open editor for selected group")
	}
	after, ok := tt.Find("Sibling")
	if !ok {
		t.Fatal("sibling disappeared during rename")
	}
	if after.Y-before.Y > 10 {
		t.Fatalf("renaming must not add a separate row above navigator: before=%+v after=%+v", before, after)
	}
	s.GroupEdit.Draft = "RenamedGroup"
	if err := tt.Click("保存组"); err != nil {
		t.Fatal(err)
	}
	if s.GroupEdit.Active || !tt.HasText("RenamedGroup") {
		t.Fatalf("inline rename was not persisted: editing=%v error=%s", s.GroupEdit.Active, s.GroupEdit.Error)
	}
	if err := tt.RightClick("RenamedGroup"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("重命名"); err != nil {
		t.Fatal(err)
	}
	s.GroupEdit.Draft = "ShouldNotExist"
	if err := tt.Click("取消编辑组"); err != nil {
		t.Fatal(err)
	}
	groups, err := svc.ListGroups(ctx)
	if err != nil || len(groups) != 1 || groups[0].Name != "RenamedGroup" || s.GroupEdit.Active {
		t.Fatalf("cancel changed persisted group: %+v err=%v editing=%v", groups, err, s.GroupEdit.Active)
	}
}

// The navigator tests own their view and inject only the menu/refresh callbacks.
// SQLite is a disposable test fixture; production navigation has no storage import.
func navigationTestState(t *testing.T) (*State, *ui.Tester, *connections.Service, func()) {
	t.Helper()
	db, err := localdb.Open(filepath.Join(t.TempDir(), "config.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	service := connections.NewService(localdb.NewProfileStore(db))
	s := New(Dependencies{})
	s.SetProfileService(service)
	refresh := func() {
		profiles, err := service.List(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		groups, err := service.ListGroups(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		s.SetProfiles(profiles, groups, 0)
	}
	s.deps.Changed = refresh
	s.deps.ContextMenu = func(services ui.Services, key string, menu *ui.Menu) {
		s.selectNode(services, key)
		if IsGroup(key) && menu.Item("重命名").Chosen() {
			s.BeginGroupEdit(GroupName(key))
		}
	}
	return s, ui.NewTester(s.View, 260, 800), service, refresh
}

func TestNavigatorLocaleAndSizeChangesKeepQualifiedSelection(t *testing.T) {
	defer i18n.SetLocaleFunc(nil)
	node := Node{Kind: "object", ProfileID: 1, Database: "app_db", Category: "表", Object: "orders"}
	s := New(Dependencies{
		SessionStatus:  func(int64) string { return "已连接" },
		Databases:      func(int64) []string { return []string{"app_db"} },
		DatabaseOpened: func(int64, string) bool { return true },
		Objects: func(int64, string) (iter.Seq[Object], bool) {
			return slices.Values([]Object{{Name: "orders", Kind: "表"}}), true
		},
	})
	s.SetProfiles([]connections.Profile{{ID: 1, Name: "Local", Kind: connections.MySQL}}, nil, 1)
	s.Tree.Open.Add(ConnectionKey(1))
	s.Tree.Open.Add(DatabaseNode(1, "app_db").Key())
	s.Tree.Open.Add((Node{Kind: "category", ProfileID: 1, Database: "app_db", Category: "表"}).Key())
	s.Key, s.Row = node.Key(), -1
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{1280, 800}, {840, 560}} {
			tt := ui.NewTester(func(c *ui.Context) {
				ui.Row(c).Fill().Children(func() {
					ui.Column(c).Width(theme.SidebarWidth).Height(float32(size[1])).Children(func() { s.View(c) })
					ui.Box(c).Grow(1)
				})
			}, size[0], size[1])
			tt.Frame()
			if !tt.HasText(locales.Text("表")) || !tt.HasText("orders") {
				t.Fatalf("%s %v lost translated categories or object names", language, size)
			}
			search, ok := tt.Find(locales.Text("搜索连接"))
			if !ok || search.Y+search.H > float32(size[1]) {
				t.Fatalf("%s %v lost reachable search field: %+v", language, size, search)
			}
			if err := tt.Click("orders"); err != nil {
				t.Fatal(err)
			}
			if s.Key != node.Key() {
				t.Fatalf("%s %v changed object identity: %s", language, size, s.Key)
			}
		}
	}
}
