package shell

import (
	"fmt"
	"testing"

	"github.com/egoist/mygo/ui"

	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func TestMySQLDataFooterRemainsReachableInFullWorkspace(t *testing.T) {
	previous := theme.CurrentMode()
	t.Cleanup(func() { theme.SetMode(previous) })
	for _, mode := range []theme.Mode{theme.ModeDark, theme.ModeLight} {
		t.Run(string(mode), func(t *testing.T) {
			theme.SetMode(mode)
			testMySQLDataFooterInFullWorkspace(t)
		})
	}
}

func testMySQLDataFooterInFullWorkspace(t *testing.T) {
	for _, size := range [][2]int{{1548, 912}, {840, 560}} {
		for _, count := range []int{0, 1, 100, 200} {
			for _, inspector := range []bool{false, true} {
				t.Run(fmt.Sprintf("%dx%d/rows%d/inspector%t", size[0], size[1], count, inspector), func(t *testing.T) {
					s := New()
					page := &mysqlsvc.MySQLDataPage{Columns: []string{"id", "label"}, ColumnKinds: []string{"bigint", "longtext"}, StableOrder: true, HasMore: true}
					for row := range count {
						page.Rows = append(page.Rows, []mysqlsvc.MySQLDataCell{{Text: fmt.Sprint(row)}, {Text: fmt.Sprintf("value-%d", row)}})
					}
					fixtureDataTab(t, s, page)
					s.activeTab, s.inspectorOn = 1, inspector
					tt := ui.NewTester(s.View, size[0], size[1])
					tt.Frame()
					for _, label := range []string{"首页数据", "上一页数据", "下一页数据", "每页行数", "查询 SQL 操作"} {
						r, found := tt.Find(label)
						if !found || r.H < theme.ControlHeight || r.X < 0 || r.Y < 0 || r.X+r.W > float32(size[0]) || r.Y+r.H > float32(size[1])-theme.StatusBarHeight {
							t.Fatalf("%s became clipped: %+v found=%t", label, r, found)
						}
					}
					if err := tt.Click("每页行数"); err != nil {
						t.Fatal(err)
					}
					if len(tt.Menu()) != 3 {
						t.Fatalf("page-size menu unreachable: %v", tt.Menu())
					}
				})
			}
		}
	}
}
