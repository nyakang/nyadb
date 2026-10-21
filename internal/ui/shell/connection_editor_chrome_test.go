package shell

import (
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestMySQLEditorSharedChromeAndTabNavigation(t *testing.T) {
	s := New()
	s.openConnectionDialog()
	s.chooseConnectionKind(connections.MySQL)
	s.nextConnectionStep()
	tt := ui.NewTester(s.ConnectionWindowView, 900, 700)

	// Every tab must retain the same shared chrome and bottom actions.
	for _, tab := range []string{"常规", "高级", "数据库", "SSL", "SSH", "HTTP", "备注"} {
		if err := tt.Click(tab); err != nil {
			t.Fatalf("navigate to %s: %v", tab, err)
		}
		if !tt.HasText("URI") || !tt.HasText("测试连接") ||
			!tt.HasText("上一步") || !tt.HasText("确定") || !tt.HasText("取消") ||
			!tt.HasText("NyaDB") || !tt.HasText("数据库") {
			t.Fatalf("common chrome disappeared on %s", tab)
		}
	}
	// The tunnel endpoint belongs to the shared diagram, not a particular
	// form. It must remain present while switching between other tabs.
	if err := tt.Click("HTTP"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("使用 HTTP 隧道"); err != nil {
		t.Fatal(err)
	}
	for _, tab := range []string{"常规", "高级", "备注", "SSL"} {
		if err := tt.Click(tab); err != nil {
			t.Fatal(err)
		}
		if !tt.HasText("HTTP 服务器") {
			t.Fatalf("shared tunnel diagram lost its HTTP node on %s", tab)
		}
	}
}
