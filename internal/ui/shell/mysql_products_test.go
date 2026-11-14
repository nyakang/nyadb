package shell

import (
	"context"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestMySQLProductNativeChooserEditorAndSavedProfileIdentity(t *testing.T) {
	for _, kind := range []connections.Kind{connections.TDSQL, connections.PolarDB, connections.GreatSQL, connections.Dolt} {
		for _, tree := range []bool{false, true} {
			layout := "grid"
			if tree {
				layout = "tree"
			}
			t.Run(string(kind)+"/"+layout, func(t *testing.T) {
				s, _, service, _ := profileTestState(t)
				s.SetConnectionWindowHost(func() {}, func() {})
				s.openConnectionDialog()
				tt := ui.NewTester(s.ConnectionWindowView, 900, 700)
				d, _ := connections.Describe(kind)
				setTestInput(t, tt, "搜索连接类型", d.Label)
				if tree {
					if err := tt.Click("列表视图"); err != nil {
						t.Fatal(err)
					}
				}
				for _, label := range []string{"选择 " + d.Label, "下一步"} {
					if err := tt.Click(label); err != nil {
						t.Fatal(err)
					}
				}
				if s.connectionEditor.Draft().Kind != kind || s.ConnectionWindowTitle() != "新建连接 ("+d.Label+")" {
					t.Fatal("chooser changed product")
				}
				setTestInput(t, tt, "连接名称", d.Label+" native workflow")
				if err := tt.Click("保存连接"); err != nil {
					t.Fatal(err)
				}
				profiles, err := service.List(context.Background())
				if err != nil || len(profiles) != 1 || profiles[0].Kind != kind || profiles[0].Port != 3306 || s.connectionDialog {
					t.Fatal("native save changed identity or failed", err)
				}
				s.startEditProfile()
				tt.Frame()
				if s.connectionEditor.Draft().Kind != kind || s.connectionEditor.Draft().ID != profiles[0].ID {
					t.Fatal("reopen changed saved product")
				}
			})
		}
	}
}
