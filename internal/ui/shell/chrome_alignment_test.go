package shell

import (
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func TestUnavailableToolbarCommandsDoNotAct(t *testing.T) {
	s := New()
	s.profileMode = true
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c)
		s.toolBar(c)
	}, 840, int(theme.ToolbarHeight))
	status := s.status
	for _, name := range []string{"函数", "用户", "其它", "查询", "备份", "自动运行", "模型"} {
		if err := tt.Click(name); err != nil {
			t.Fatal(err)
		}
		if s.lastAction != "" || s.status != status || len(s.tabs) != 1 {
			t.Fatalf("unsupported command %s changed workbench state", name)
		}
	}
	if err := tt.Click("新建查询"); err != nil {
		t.Fatal(err)
	}
	if s.currentTab().kind != tabSQL {
		t.Fatal("supported command was disabled with unavailable features")
	}
}

func TestToolbarModeHasNeutralSelectionAndPinnedInspector(t *testing.T) {
	before := theme.CurrentMode()
	defer theme.SetMode(before)
	theme.SetMode(theme.ModeDark)
	s := New()
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c)
		s.toolBar(c)
	}, 840, int(theme.ToolbarHeight))
	r, ok := tt.Find("表")
	if !ok {
		t.Fatal("selected mode is missing")
	}
	pixel := tt.Image().RGBAAt(int(r.X+5), int(r.Y+5))
	if pixel.R != theme.ToolbarSelected.R || pixel.G != theme.ToolbarSelected.G || pixel.B != theme.ToolbarSelected.B {
		t.Fatalf("toolbar selection should use a neutral mode surface: %v", pixel)
	}
	// A narrow toolbar puts trailing actions in MyGo's native overflow, but
	// the inspector toggle remains a reachable independent control.
	tt.SetSize(420, int(theme.ToolbarHeight))
	r, ok = tt.Find("信息")
	if !ok || r.X < 0 || r.X+r.W > 420 {
		t.Fatalf("inspector toggle was lost during overflow: %+v", r)
	}
	if err := tt.Click("信息"); err != nil {
		t.Fatal(err)
	}
	if s.inspectorOn {
		t.Fatal("pinned inspector toggle did not activate")
	}
}
