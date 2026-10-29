package shell

import (
	"context"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

func TestNavigatorGroupRenameEditsInsideOriginalTreeRow(t *testing.T) {
	s, tt, svc, _ := profileTestState(t)
	ctx := context.Background()
	if err := svc.CreateGroup(ctx, "OldGroup"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Create(ctx, connections.Profile{Name: "Sibling", Kind: connections.MySQL, Host: "localhost", Port: 3306}); err != nil {
		t.Fatal(err)
	}
	s.loadProfiles()
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
	if !s.groupEditing || s.groupEditOriginal != "OldGroup" {
		t.Fatal("rename did not open editor for selected group")
	}
	after, ok := tt.Find("Sibling")
	if !ok {
		t.Fatal("sibling disappeared during rename")
	}
	if after.Y-before.Y > 10 {
		t.Fatalf("renaming must not add a separate row above navigator: before=%+v after=%+v", before, after)
	}
	s.groupDraft = "RenamedGroup"
	if err := tt.Click("保存组"); err != nil {
		t.Fatal(err)
	}
	if s.groupEditing || !tt.HasText("RenamedGroup") {
		t.Fatalf("inline rename was not persisted: editing=%v error=%s", s.groupEditing, s.groupError)
	}
	if err := tt.RightClick("RenamedGroup"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("重命名"); err != nil {
		t.Fatal(err)
	}
	s.groupDraft = "ShouldNotExist"
	if err := tt.Click("取消编辑组"); err != nil {
		t.Fatal(err)
	}
	groups, err := svc.ListGroups(ctx)
	if err != nil || len(groups) != 1 || groups[0].Name != "RenamedGroup" || s.groupEditing {
		t.Fatalf("cancel changed persisted group: %+v err=%v editing=%v", groups, err, s.groupEditing)
	}
}

func TestActiveTabNavigationRestoresPriorFiltersOnRequest(t *testing.T) {
	s, tt := mysqlObjectTestState(t)
	id := s.mysqlSessionProfileID
	s.mysqlObjectCache["app_db"] = []mysqlsvc.MySQLDBObject{{Name: "orders", Kind: "表"}}
	s.tabs = append(s.tabs, &tab{kind: tabMySQLData, title: "orders", dataProfileID: id, dataDatabase: "app_db", dataTable: "orders"})
	s.activeTab = len(s.tabs) - 1
	s.search = "hidden"
	s.profileEngine = connections.SQLite
	s.profileStarredOnly = true
	s.navigateToActiveTab(ui.Services{})
	if s.navigationFilterRestore == nil || s.search != "" || s.profileEngine != "" || s.profileStarredOnly {
		t.Fatalf("active navigation did not snapshot temporary filter: %+v", s.navigationFilterRestore)
	}
	tt.Frame()
	if err := tt.Click("恢复之前的筛选"); err != nil {
		t.Fatal(err)
	}
	if s.navigationFilterRestore != nil || s.search != "hidden" || s.profileEngine != connections.SQLite || !s.profileStarredOnly {
		t.Fatalf("filter was not restored: search=%q type=%s starred=%v", s.search, s.profileEngine, s.profileStarredOnly)
	}
	s.navigateToActiveTab(ui.Services{})
	if s.navigationFilterRestore == nil {
		t.Fatal("second active navigation failed to capture filters")
	}
	s.search = "new manual search"
	s.changeProfileFilter()
	if s.navigationFilterRestore != nil {
		t.Fatal("manual search must supersede a temporary filter restore")
	}
}
