package dataworkspace

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func TestMySQLDataReadStatusSurvivesFailureAndCancellation(t *testing.T) {
	s := New(Dependencies{})
	session := s.session()
	s.deps.Session = func() Session { return session }
	doc := &Document{profileID: 12, limit: 100, loading: true, request: 3}
	s.documents[doc.id] = doc
	session.ProfileID, session.Generation, session.Connected = 12, 2, true
	completed := time.Date(2026, 10, 26, 8, 30, 0, 0, time.Local)
	page := &mysqlsvc.MySQLDataPage{QuerySQL: "SELECT `id` FROM `db`.`t` LIMIT ? OFFSET ?", CompletedAt: completed, Duration: 12 * time.Millisecond}
	result := make(chan mysqlDataResult, 1)
	doc.results = result
	result <- mysqlDataResult{generation: 2, profileID: 12, request: 3, page: page}
	s.consumePages()
	if doc.readInfo.CompletedAt != completed || doc.readInfo.QuerySQL != page.QuerySQL {
		t.Fatal("completed execution details were not accepted")
	}
	doc.loading, doc.page = true, nil
	result = make(chan mysqlDataResult, 1)
	doc.results = result
	result <- mysqlDataResult{generation: 2, profileID: 12, request: doc.request, err: errors.New("read failed")}
	s.consumePages()
	if doc.readInfo.CompletedAt != completed || !strings.HasPrefix(mysqlDataQueryStatus(doc), "上次成功查询") {
		t.Fatal("failed read replaced the last successful execution")
	}
	doc.loading = true
	s.cancelMySQLDataRead(doc)
	if doc.loading || doc.err != "" || !strings.HasPrefix(mysqlDataPageStatus(doc), "读取已取消") || doc.readInfo.CompletedAt != completed {
		t.Fatal("cancellation lost the successful execution or became a query failure")
	}
}

func TestMySQLDataPageSizeResetsCursorAndUsesBoundedLimit(t *testing.T) {
	s := New(Dependencies{})
	session := s.session()
	s.deps.Session = func() Session { return session }
	session.ProfileID, session.Connected = 12, true
	doc := &Document{profileID: 12, database: "db", table: "t", limit: 100,
		offset: 200, after: &mysqlsvc.MySQLDataCursor{}, cursorHistory: map[int64]*mysqlsvc.MySQLDataCursor{100: {}}, selectedRow: 4}
	s.documents[doc.id] = doc
	requested := make(chan mysqlsvc.MySQLDataOptions, 1)
	s.AdvancedLoader = func(_ context.Context, _ int64, _, _ string, options mysqlsvc.MySQLDataOptions) (*mysqlsvc.MySQLDataPage, error) {
		requested <- options
		return &mysqlsvc.MySQLDataPage{}, nil
	}
	tt := ui.NewTester(func(c *ui.Context) {
		if !doc.loading {
			s.changeMySQLDataPageSize(c.Services(), doc, 50)
		}
	}, 400, 200)
	_ = tt
	select {
	case options := <-requested:
		if options.Limit != 50 || options.Offset != 0 || options.After != nil || !options.UseKeyset || doc.cursorHistory != nil || doc.selectedRow != -1 {
			t.Fatalf("page-size change retained an incompatible position: %+v", options)
		}
	case <-time.After(time.Second):
		t.Fatal("page-size change did not request data")
	}
	s.Cancel(doc)
	s.changeMySQLDataPageSize(ui.Services{}, doc, 1000)
	if doc.limit != 50 {
		t.Fatal("unbounded page size accepted")
	}
}

func TestMySQLGridEmptyAndTruncatedPreviewsKeepTheirValues(t *testing.T) {
	s := New(Dependencies{})
	doc := alignedDataTestTab()
	doc.page.Rows = [][]mysqlsvc.MySQLDataCell{{{Null: true}, {Text: ""}}, {{Text: "2"}, {Text: "short preview", Truncated: true}}}
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.View(c, doc) }, 650, 400)
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
	s := New(Dependencies{})
	doc := alignedDataTestTab()
	doc.page.QuerySQL = "SELECT `id`, SUBSTRING(`label`, 1, 2049) AS `label` FROM `db`.`records` WHERE `id` > ? LIMIT ? OFFSET ?"
	doc.page.CompletedAt = time.Now()
	doc.readInfo.accept(doc.page)
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.View(c, doc) }, 650, 400)
	if err := tt.Click("查询 SQL 操作"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("复制参数化 SQL"); err != nil {
		t.Fatal(err)
	}
	if tt.Clipboard() != doc.page.QuerySQL {
		t.Fatalf("SQL copy changed the executed template: %q", tt.Clipboard())
	}
}
