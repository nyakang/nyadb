package shell

import "testing"

func TestMySQLConfigurationSwitchRetainsCommonTab(t *testing.T) {
	s, tt, _, _ := editMySQLProfileTester(t)
	if err := tt.Click("添加连接配置文件"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("高级"); err != nil {
		t.Fatal(err)
	}
	if s.connectionEditorTab != 1 {
		t.Fatal("Advanced tab was not selected")
	}
	if err := tt.Click("切换配置文件：主要配置文件 (Active)"); err != nil {
		t.Fatal(err)
	}
	if s.connectionEditorTab != 1 || !tt.HasText("设置位置：") {
		t.Fatal("switching to main configuration changed the active tab")
	}
	if err := tt.Click("切换配置文件：New Profile"); err != nil {
		t.Fatal(err)
	}
	if s.connectionEditorTab != 1 || !tt.HasText("设置位置：") {
		t.Fatal("switching to alternative configuration changed the active tab")
	}
}
