package shell

import (
	"context"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestCloudberryNativeChooserEditorAndSavedProfileIdentity(t *testing.T) {
	for _, tree := range []bool{false, true} {
		t.Run(map[bool]string{false: "grid", true: "tree"}[tree], func(t *testing.T) {
			s, _, service, _ := profileTestState(t)
			s.SetConnectionWindowHost(func() {}, func() {})
			s.openConnectionDialog()
			tt := ui.NewTester(s.ConnectionWindowView, 900, 700)
			setTestInput(t, tt, "搜索连接类型", "Cloudberry")
			if tree {
				if err := tt.Click("列表视图"); err != nil {
					t.Fatal(err)
				}
			}
			for _, label := range []string{"选择 Apache Cloudberry", "下一步"} {
				if err := tt.Click(label); err != nil {
					t.Fatal(err)
				}
			}
			if s.connectionEditor.Draft().Kind != connections.Cloudberry || s.ConnectionWindowTitle() != "新建连接 (Apache Cloudberry)" {
				t.Fatal("chooser changed original product")
			}
			setTestInput(t, tt, "连接名称", "Cloudberry native workflow")
			for _, label := range []string{"高级", "SSL", "常规"} {
				if err := tt.Click(label); err != nil {
					t.Fatal(err)
				}
			}
			if err := tt.Click("保存连接"); err != nil {
				t.Fatal(err)
			}
			profiles, err := service.List(context.Background())
			if err != nil || len(profiles) != 1 || profiles[0].Kind != connections.Cloudberry || profiles[0].Port != 5432 || s.connectionDialog {
				t.Fatal("saved profile changed product or failed", err, s.connectionEditor.Error())
			}
			s.startEditProfile()
			tt.Frame()
			if s.connectionEditor.Draft().Kind != connections.Cloudberry || s.connectionEditor.Draft().ID != profiles[0].ID {
				t.Fatal("reopen changed original profile")
			}
		})
	}
}
