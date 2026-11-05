package navigation

import (
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func TestNavigatorDisclosureUsesNativeTriangle(t *testing.T) {
	var tree ui.OutlineState[string]
	ConfigureDisclosure(&tree)
	if tree.Disclosure != ui.OutlineTriangle {
		t.Fatalf("disclosure = %v, want triangle", tree.Disclosure)
	}
	if tree.DisclosureSize != 10 {
		t.Fatalf("disclosure gutter = %v, want 10 DIPs", tree.DisclosureSize)
	}
	ConfigureDisclosure(&tree)
	if tree.Disclosure != ui.OutlineTriangle || tree.DisclosureSize != 10 {
		t.Fatal("enabling disclosure should be idempotent")
	}
}

func TestNavigatorDisclosureNilState(t *testing.T) {
	ConfigureDisclosure(nil)
}

func TestConnectionDisclosureFollowsSessionStatus(t *testing.T) {
	status, reads := "未连接", 0
	s := New(Dependencies{
		SessionStatus: func(int64) string { return status },
		Databases:     func(int64) []string { reads++; return []string{"app_db"} },
		Queries:       func(int64, string) []Object { return []Object{{Name: "Saved", Kind: "q"}} },
	})
	s.SetProfiles([]connections.Profile{{ID: 1, Name: "Local", Kind: connections.MySQL}}, nil, 1)
	connection := ConnectionKey(1)
	s.Tree.Open.Add(connection)
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.View(c) }, 260, 400)
	for _, next := range []string{"未连接", "连接中", "连接失败", "已连接", "未连接"} {
		status = next
		before := reads
		tt.Frame()
		children := s.Children(connection)
		if status == "已连接" {
			if len(children) != 2 || Parse(children[0]).Kind != "database" || Parse(children[1]).Category != "查询" || !tt.HasText("app_db") {
				t.Fatalf("connected directory missing: %v", children)
			}
		} else if children != nil || s.Tree.Rows() != 1 || tt.HasText("app_db") || reads != before {
			t.Fatalf("%s exposed a subtree or read metadata: children=%v rows=%d reads=%d", status, children, s.Tree.Rows(), reads-before)
		}
	}
}
