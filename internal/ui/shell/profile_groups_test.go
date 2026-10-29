package shell

import (
	"context"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestPersistentGroupTreeInspectorAndConnectionPlacement(t *testing.T) {
	s, tt, service, _ := profileTestState(t)
	first, err := service.Create(context.Background(), connections.Profile{Name: "Local", Kind: connections.MySQL, Host: "localhost", Port: 3306})
	if err != nil {
		t.Fatal(err)
	}
	s.loadProfiles()
	if err := tt.RightClick("我的连接"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("新建组"); err != nil {
		t.Fatal(err)
	}
	s.navigation.GroupEdit.Draft = "开发库"
	if err := tt.Click("保存组"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("开发库") || s.navigation.Group != "开发库" {
		t.Fatalf("missing folder: %+v", s.navigation.Groups())
	}
	s.moveProfileToGroup(first.ID, "开发库")
	tt.Frame()
	if err := tt.Click("开发库"); err != nil {
		t.Fatal(err)
	}
	if s.navigation.Group != "开发库" || s.selectedProfileID != 0 {
		t.Fatalf("folder should select independent navigation context: group=%q id=%d", s.navigation.Group, s.selectedProfileID)
	}
	if !tt.HasText("虚拟组 (1 对象)") {
		t.Fatal("folder inspector did not show actual membership count")
	}
	// A newly created connection inherits the selected virtual folder.
	s.openConnectionDialog()
	s.chooseConnectionKind(connections.MySQL)
	s.nextConnectionStep()
	s.connectionEditor.Draft().Name = "Grouped new"
	s.saveConnectionDraft()
	p, ok := s.currentProfile()
	if !ok || p.GroupName != "开发库" {
		t.Fatalf("new profile not placed in selected group: %+v", p)
	}
	if err := tt.RightClick("开发库"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("重命名"); err != nil {
		t.Fatal(err)
	}
	s.navigation.GroupEdit.Draft = "研发组"
	if err := tt.Click("保存组"); err != nil {
		t.Fatal(err)
	}
	p, err = service.Get(context.Background(), p.ID)
	if err != nil || p.GroupName != "研发组" {
		t.Fatalf("rename lost assigned profile: %+v %v", p, err)
	}
	if err := tt.RightClick("研发组"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("删除组"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("删除组"); err != nil {
		t.Fatal(err)
	}
	p, err = service.Get(context.Background(), p.ID)
	if err != nil || p.GroupName != "" {
		t.Fatalf("delete group removed or orphaned connection: %+v %v", p, err)
	}
}
