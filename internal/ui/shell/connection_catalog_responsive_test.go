package shell

import (
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestConnectionCatalogMinimumWindow(t *testing.T) {
	s := New()
	tt := ui.NewTester(s.View, 840, 560)
	if err := tt.Click("连接"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("连接筛选") || !tt.HasText("全部") {
		t.Fatal("catalog sections missing at minimum window size")
	}
	if err := tt.Click("选择 SQLite"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("下一步"); err != nil {
		t.Fatal(err)
	}
	if s.connectionStep != 1 || s.connectionDraft.Kind != connections.SQLite {
		t.Fatal("minimum window failed to enter native connection editor")
	}
}
