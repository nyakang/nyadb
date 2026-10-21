package shell

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// Buttons in the configuration sidebar are native MyGo controls, rather than
// pointer-only hit targets. Arrow-key navigation changes only the
// highlighted editor row; it must never silently change the Active target.
func TestMySQLConfigurationSidebarKeyboardSelection(t *testing.T) {
	s, tt, _, _ := editMySQLProfileTester(t)
	s.addConnectionConfiguration()
	tt.Frame()
	if err := tt.Click("切换配置文件：主要配置文件 (Active)"); err != nil {
		t.Fatal(err)
	}
	if s.connectionActiveConfig != 0 {
		t.Fatal("primary not selected")
	}
	tt.Key(0, ui.KeyDown)
	if s.connectionActiveConfig != 1 || s.connectionActiveTarget != 0 {
		t.Fatalf("keyboard selection incorrectly changed Active or failed to navigate: selected=%d active=%d", s.connectionActiveConfig, s.connectionActiveTarget)
	}
	if err := tt.Click("切换配置文件：New Profile"); err != nil {
		t.Fatal(err)
	}
	tt.Key(0, ui.KeyUp)
	if s.connectionActiveConfig != 0 || s.connectionActiveTarget != 0 {
		t.Fatal("Up arrow did not restore primary without changing Active")
	}
}
