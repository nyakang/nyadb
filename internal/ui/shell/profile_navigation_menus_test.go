package shell

import (
	"context"
	"slices"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
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
	s.explorerTree.Open.Add(groupNode("开发库"))
	tt.Frame()
	tt.RightClickAt(160, 325) // empty navigator area; not an Outline row
	if len(tt.Menu()) != 0 {
		t.Fatalf("blank menu contains connection-specific actions: %v", tt.Menu())
	}
	tt.CloseMenu()
	if err := tt.RightClick("开发库"); err != nil {
		t.Fatal(err)
	}
	if s.selectedGroup != "开发库" {
		t.Fatalf("group right-click should select the group: %q", s.selectedGroup)
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
	if !s.profileStarredOnly || !tt.HasText("Local") {
		t.Fatal("star-only filter failed")
	}
	if err := tt.Click("全部折叠"); err != nil {
		t.Fatal(err)
	}
	if s.explorerTree.Open.Len() != 0 {
		t.Fatal("tree wasn't collapsed")
	}
	if err := tt.Click("显示已星标"); err != nil {
		t.Fatal(err)
	}
	if s.profileStarredOnly {
		t.Fatal("star filter was not cleared")
	}
	if err := tt.RightClick("开发库"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("重命名"); err != nil {
		t.Fatal(err)
	}
	if !s.groupEditing || s.groupDraft != "开发库" {
		t.Fatal("group rename did not target the selected group")
	}
}

func TestNavigatorHeaderRevealsOnlySettingsOnHover(t *testing.T) {
	_, tt, _, _ := profileTestState(t)
	if tt.HasText("新建组") || tt.HasText("添加连接") {
		t.Fatal("always-visible creation controls are not allowed in header")
	}
	tt.Move(260, 103)
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
	if !slices.Contains(tt.Menu(), "新建连接…") {
		t.Fatal("header settings context menu unavailable")
	}
	tt.CloseMenu()
	if _, _, _, yes := s.activeTabObject(); yes {
		t.Fatal("object tab must not be navigable")
	}
}
