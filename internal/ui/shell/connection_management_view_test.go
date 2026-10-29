package shell

import (
	"context"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestConnectionManagementEntryAndDynamicSessionMenu(t *testing.T) {
	s, tt, svc, _ := profileTestState(t)
	p, err := svc.Create(context.Background(), connections.Profile{
		Name: "MenuTarget", Kind: connections.MySQL, Host: "localhost", Port: 3306,
	})
	if err != nil {
		t.Fatal(err)
	}
	s.loadProfiles()
	tt.Frame()
	if err := tt.RightClick("我的连接"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("管理连接…"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if s.currentTab().kind != tabConnections || !tt.HasText("MenuTarget") {
		t.Fatal("management menu did not open real connection list")
	}
	s.activeTab = 0
	s.sessions.Current().ProfileID = p.ID
	s.sessions.Current().Status = "连接中"
	tt.Frame()
	if err := tt.RightClick("MenuTarget"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("删除连接"); err == nil {
		t.Fatal("delete must be disabled while connecting")
	}
	tt.CloseMenu()
	s.sessions.Current().Status = "已连接"
	tt.Frame()
	if err := tt.RightClick("MenuTarget"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("新建查询"); err != nil {
		t.Fatal(err)
	}
	if s.currentTab().kind != tabSQL {
		t.Fatal("connected menu did not create SQL tab")
	}
}
