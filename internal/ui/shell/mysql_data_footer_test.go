package shell

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
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
					doc := alignedDataTestTab()
					doc.id, doc.dataSelectedRow = 1, -1
					doc.dataShowFieldTypes = true
					doc.dataPage.ColumnKinds = []string{"bigint", "longtext"}
					doc.dataPage.Rows = nil
					for row := range count {
						doc.dataPage.Rows = append(doc.dataPage.Rows, []connections.MySQLDataCell{{Text: fmt.Sprint(row)}, {Text: fmt.Sprintf("value-%d", row)}})
					}
					doc.dataPage.HasMore = true
					s.tabs = append(s.tabs[:1], doc)
					s.activeTab, s.inspectorOn = 1, inspector
					tt := ui.NewTester(s.View, size[0], size[1])
					tt.Frame()
					for _, label := range []string{"首页数据", "上一页数据", "下一页数据", "每页行数", "查询 SQL 操作"} {
						r, found := tt.Find(label)
						if !found || r.H < theme.ControlHeight || r.X < 0 || r.Y < 0 || r.X+r.W > float32(size[0]) || r.Y+r.H > float32(size[1])-theme.StatusBarHeight {
							t.Fatalf("%s became clipped: %+v found=%t", label, r, found)
						}
					}
					r, found := tt.Find(mysqlDataPageStatus(doc))
					if !found || r.W <= 0 || r.H <= 0 {
						t.Fatalf("page status lost visible area: %+v", r)
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

func TestMySQLDataReadStatusSurvivesFailureAndCancellation(t *testing.T) {
	s := New()
	doc := &tab{kind: tabMySQLData, dataProfileID: 12, dataLimit: 100, dataLoading: true, dataRequest: 3}
	s.tabs, s.mysqlSessionProfileID, s.mysqlSessionGeneration = []*tab{doc}, 12, 2
	completed := time.Date(2026, 10, 26, 8, 30, 0, 0, time.Local)
	page := &connections.MySQLDataPage{QuerySQL: "SELECT `id` FROM `db`.`t` LIMIT ? OFFSET ?", CompletedAt: completed, Duration: 12 * time.Millisecond}
	result := make(chan mysqlDataResult, 1)
	doc.dataResults = result
	result <- mysqlDataResult{generation: 2, profileID: 12, request: 3, page: page}
	s.consumeMySQLData()
	if doc.dataReadInfo.CompletedAt != completed || doc.dataReadInfo.QuerySQL != page.QuerySQL {
		t.Fatal("completed execution details were not accepted")
	}
	doc.dataLoading, doc.dataPage = true, nil
	result = make(chan mysqlDataResult, 1)
	doc.dataResults = result
	result <- mysqlDataResult{generation: 2, profileID: 12, request: doc.dataRequest, err: errors.New("read failed")}
	s.consumeMySQLData()
	if doc.dataReadInfo.CompletedAt != completed || !strings.HasPrefix(mysqlDataQueryStatus(doc), "上次成功查询") {
		t.Fatal("failed read replaced the last successful execution")
	}
	doc.dataLoading = true
	s.cancelMySQLDataRead(doc)
	if doc.dataLoading || doc.dataErr != "" || !strings.HasPrefix(mysqlDataPageStatus(doc), "读取已取消") || doc.dataReadInfo.CompletedAt != completed {
		t.Fatal("cancellation lost the successful execution or became a query failure")
	}
}

func TestMySQLDataPageSizeResetsCursorAndUsesBoundedLimit(t *testing.T) {
	s := New()
	s.mysqlSessionProfileID, s.mysqlSessionStatus = 12, "已连接"
	doc := &tab{kind: tabMySQLData, dataProfileID: 12, dataDatabase: "db", dataTable: "t", dataLimit: 100,
		dataOffset: 200, dataAfter: &connections.MySQLDataCursor{}, dataCursorHistory: map[int64]*connections.MySQLDataCursor{100: {}}, dataSelectedRow: 4}
	s.tabs = []*tab{doc}
	requested := make(chan connections.MySQLDataOptions, 1)
	s.mysqlDataAdvancedLoader = func(_ context.Context, _ int64, _, _ string, options connections.MySQLDataOptions) (*connections.MySQLDataPage, error) {
		requested <- options
		return &connections.MySQLDataPage{}, nil
	}
	tt := ui.NewTester(func(c *ui.Context) {
		if !doc.dataLoading {
			s.changeMySQLDataPageSize(c.Services(), doc, 50)
		}
	}, 400, 200)
	_ = tt
	select {
	case options := <-requested:
		if options.Limit != 50 || options.Offset != 0 || options.After != nil || !options.UseKeyset || doc.dataCursorHistory != nil || doc.dataSelectedRow != -1 {
			t.Fatalf("page-size change retained an incompatible position: %+v", options)
		}
	case <-time.After(time.Second):
		t.Fatal("page-size change did not request data")
	}
	s.cancelMySQLData(doc)
	s.changeMySQLDataPageSize(ui.Services{}, doc, 1000)
	if doc.dataLimit != 50 {
		t.Fatal("unbounded page size accepted")
	}
}

func TestMySQLGridEmptyAndTruncatedPreviewsKeepTheirValues(t *testing.T) {
	s := New()
	doc := alignedDataTestTab()
	doc.dataPage.Rows = [][]connections.MySQLDataCell{{{Null: true}, {Text: ""}}, {{Text: "2"}, {Text: "short preview", Truncated: true}}}
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.mysqlDataWorkspace(c, doc) }, 650, 400)
	if !tt.HasText("NULL") || !tt.HasText("short preview") || tt.HasText("short preview …") || tt.HasText("\"\"") {
		t.Fatalf("grid changed empty or preview content: %v", tt.Texts())
	}
	if err := tt.Click("short preview"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("复制所选数据预览"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tt.Clipboard(), "预览已截断") {
		t.Fatal("copy omitted the preview truncation warning")
	}
}

func TestMySQLQueryFooterCopiesExecutedTemplate(t *testing.T) {
	s := New()
	doc := alignedDataTestTab()
	doc.dataPage.QuerySQL = "SELECT `id`, SUBSTRING(`label`, 1, 2049) AS `label` FROM `db`.`records` WHERE `id` > ? LIMIT ? OFFSET ?"
	doc.dataPage.CompletedAt = time.Now()
	doc.dataReadInfo.accept(doc.dataPage)
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.mysqlDataWorkspace(c, doc) }, 650, 400)
	if err := tt.Click("查询 SQL 操作"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("复制参数化 SQL"); err != nil {
		t.Fatal(err)
	}
	if tt.Clipboard() != doc.dataPage.QuerySQL {
		t.Fatalf("SQL copy changed the executed template: %q", tt.Clipboard())
	}
}
