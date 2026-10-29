package shell

import (
	"context"
	"slices"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/navigation"
)

func TestNavigatorContextMenusAreScopedByHitTarget(t *testing.T) {
	s, tt, svc, _ := profileTestState(t)
	ctx := context.Background()
	if err := svc.CreateGroup(ctx, "开发库"); err != nil {
		t.Fatal(err)
	}
	p, err := svc.Create(ctx, connections.Profile{Name: "Local", GroupName: "开发库", Kind: connections.MySQL, Host: "localhost", Port: 3306})
	if err != nil {
		t.Fatal(err)
	}
	s.loadProfiles()
	s.navigation.Tree.Open.Add(navigation.GroupKey("开发库"))
	tt.Frame()
	tt.RightClickAt(160, 325) // empty navigator area; not an Outline row
	if len(tt.Menu()) != 0 {
		t.Fatalf("blank menu contains connection-specific actions: %v", tt.Menu())
	}
	tt.CloseMenu()
	if err := tt.RightClick("开发库"); err != nil {
		t.Fatal(err)
	}
	if s.navigation.Group != "开发库" {
		t.Fatalf("group right-click should select the group: %q", s.navigation.Group)
	}
	if !slices.Contains(tt.Menu(), "重命名") || slices.Contains(tt.Menu(), "编辑连接") {
		t.Fatalf("group menu wrong: %v", tt.Menu())
	}
	tt.CloseMenu()
	if err := tt.RightClick("Local"); err != nil {
		t.Fatal(err)
	}
	if s.selectedProfileID != p.ID {
		t.Fatalf("connection right-click should select the profile: %d", s.selectedProfileID)
	}
	if !slices.Contains(tt.Menu(), "编辑连接") || !slices.Contains(tt.Menu(), "添加星标") || slices.Contains(tt.Menu(), "重命名") {
		t.Fatalf("asset menu wrong: %v", tt.Menu())
	}
	if err := tt.ChooseMenuItem("添加星标"); err != nil {
		t.Fatal(err)
	}
	saved, err := svc.Get(ctx, p.ID)
	if err != nil || !saved.Starred {
		t.Fatalf("favorite not saved: %+v %v", saved, err)
	}
	if err := tt.Click("显示已星标"); err != nil {
		t.Fatal(err)
	}
	if !s.navigation.Filter.StarredOnly || !tt.HasText("Local") {
		t.Fatal("star-only filter failed")
	}
	if err := tt.Click("全部折叠"); err != nil {
		t.Fatal(err)
	}
	if s.navigation.Tree.Open.Len() != 0 {
		t.Fatal("tree wasn't collapsed")
	}
	if err := tt.Click("显示已星标"); err != nil {
		t.Fatal(err)
	}
	if s.navigation.Filter.StarredOnly {
		t.Fatal("star filter was not cleared")
	}
	if err := tt.RightClick("开发库"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("重命名"); err != nil {
		t.Fatal(err)
	}
	if !s.navigation.GroupEdit.Active || s.navigation.GroupEdit.Draft != "开发库" {
		t.Fatal("group rename did not target the selected group")
	}
}

func TestNavigatorRightClickUnselectedRowAcrossItsWidth(t *testing.T) {
	s, tt, svc, _ := profileTestState(t)
	if err := svc.CreateGroup(context.Background(), "Group"); err != nil {
		t.Fatal(err)
	}
	p, err := svc.Create(context.Background(), connections.Profile{Name: "Local", Kind: connections.MySQL, Host: "localhost", Port: 3306})
	if err != nil {
		t.Fatal(err)
	}
	s.loadProfiles()
	tt.Frame()
	for _, name := range []string{"Group", "Local"} {
		for _, x := range []float32{16, 40, 220} {
			// Right-click a different row without first left-clicking it.
			other := "Local"
			if name == "Local" {
				other = "Group"
			}
			if err := tt.Click(other); err != nil {
				t.Fatal(err)
			}
			tt.Frame()
			r, ok := tt.Find(name)
			if !ok {
				t.Fatalf("missing row %s", name)
			}
			tt.RightClickAt(x, r.Y+r.H/2)
			command := "重命名"
			if name == "Local" {
				command = "编辑连接"
			}
			if !slices.Contains(tt.Menu(), command) {
				t.Fatalf("right-click %s at x=%v did not open its menu: %v", name, x, tt.Menu())
			}
			if name == "Local" && s.selectedProfileID != p.ID || name == "Group" && s.navigation.Group != name {
				t.Fatalf("right-click %s failed to select its target", name)
			}
			tt.CloseMenu()
			tt.Frame()
			if name == "Local" && s.selectedProfileID != p.ID || name == "Group" && s.navigation.Group != name {
				t.Fatalf("right-click %s lost its target after menu dismissal", name)
			}
		}
	}
	if err := tt.Click("Group"); err != nil {
		t.Fatal(err)
	}
	r, _ := tt.Find("Local")
	tt.RightClickAt(220, r.Y+r.H/2)
	if err := tt.ChooseMenuItem("添加星标"); err != nil {
		t.Fatal(err)
	}
	saved, err := svc.Get(context.Background(), p.ID)
	if err != nil || !saved.Starred || s.mysqlSessionProfileID != 0 {
		t.Fatalf("context action must target the clicked connection without connecting: starred=%v err=%v", saved.Starred, err)
	}
	tt.Key(0, ui.KeyUp)
	if s.navigation.Group != "Group" {
		t.Fatal("keyboard navigation did not continue from the right-click target")
	}
	if err := tt.ClickWith(ui.Ctrl, "Local"); err != nil {
		t.Fatal(err)
	}
	selected := s.navigation.Tree.List.Selection.(*ui.Selection[string])
	if selected.Len() != 1 || !selected.Has(navigation.ConnectionKey(p.ID)) {
		t.Fatal("navigator must retain one selected identity")
	}
}

func TestNavigatorHeaderRevealsOnlySettingsOnHover(t *testing.T) {
	_, tt, _, _ := profileTestState(t)
	if tt.HasText("新建组") || tt.HasText("添加连接") {
		t.Fatal("always-visible creation controls are not allowed in header")
	}
	tt.Move(220, 108)
	tt.Frame()
	if !tt.HasText("连接面板设置") {
		t.Fatal("hover should reveal settings menu button")
	}
	if err := tt.Click("连接面板设置"); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(tt.Menu(), "新建组") {
		t.Fatalf("missing settings commands: %v", tt.Menu())
	}
}

func TestNavigatorFilterAndActiveObjectGuard(t *testing.T) {
	s, tt, svc, _ := profileTestState(t)
	ctx := context.Background()
	for _, p := range []connections.Profile{
		{Name: "Mysql", Kind: connections.MySQL, Host: "localhost", Port: 3306},
		{Name: "SQLite", Kind: connections.SQLite, Database: "/tmp/unused-nyadb.db"},
	} {
		if _, err := svc.Create(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	s.loadProfiles()
	tt.Frame()
	if err := tt.Click("筛选连接"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("SQLite"); err != nil {
		t.Fatal(err)
	}
	if tt.HasText("Mysql") || !tt.HasText("SQLite") {
		t.Fatal("engine filter did not limit visible tree")
	}
	if err := tt.Click("筛选连接"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("所有连接类型"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("Mysql") {
		t.Fatal("reset filter lost MySQL")
	}
	if err := tt.RightClick("我的连接"); err != nil {
		t.Fatal(err)
	}
	if s.selectedProfileID != 0 || s.navigation.Group != "" || s.navigation.Key != "" {
		t.Fatal("header context menu must select the navigation root")
	}
	if !slices.Contains(tt.Menu(), "新建连接…") {
		t.Fatal("header settings context menu unavailable")
	}
	tt.CloseMenu()
	tt.Frame()
	if s.selectedProfileID != 0 || s.navigation.Key != "" {
		t.Fatal("root selection reverted after closing its menu")
	}
	if _, _, _, yes := s.activeTabObject(); yes {
		t.Fatal("object tab must not be navigable")
	}
}
