package shell

import (
	"context"
	"errors"
	"strings"
	"testing"

	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

func TestSQLTabLimitErrorVisibleAndRetryableInWorkspace(t *testing.T) {
	s, tt, _ := sqlUITestState(t)
	q := s.currentTab()
	q.editor.Buffer.Set("SELECT 1")
	s.mysqlSQLExecutor = func(context.Context, int64, string, string) (mysqlsvc.MySQLSQLResult, error) {
		return mysqlsvc.MySQLSQLResult{}, mysqlsvc.ErrMySQLSQLSessionLimit
	}
	if err := tt.Click("运行当前或选中 SQL"); err != nil {
		t.Fatal(err)
	}
	waitSQLUI(t, s, tt, q)
	if !strings.Contains(q.sqlError, "关闭其他查询标签后重试") ||
		!strings.Contains(s.status, "SQL 执行失败") {
		t.Fatalf("quota error not reported: %q, %q", q.sqlError, s.status)
	}
	if q.editor.Buffer.String() != "SELECT 1" {
		t.Fatal("quota rejection discarded SQL editor text")
	}
	s.mysqlSQLExecutor = func(context.Context, int64, string, string) (mysqlsvc.MySQLSQLResult, error) {
		return mysqlsvc.MySQLSQLResult{Columns: []string{"id"}, Rows: [][]string{{"1"}}}, nil
	}
	if err := tt.Click("运行当前或选中 SQL"); err != nil {
		t.Fatal(err)
	}
	waitSQLUI(t, s, tt, q)
	if q.sqlError != "" || q.sqlResult == nil || q.sqlResult.Rows[0][0] != "1" {
		t.Fatalf("tab did not recover after budget became available: %q, %+v", q.sqlError, q.sqlResult)
	}
}

func TestSQLTabLimitPreparationErrorRemainsDistinctFromLostSession(t *testing.T) {
	s := New()
	s.openQuery()
	q := s.currentTab()
	done := make(chan error, 1)
	done <- errors.New("limit reached")
	q.sqlPrepareResults = done
	s.consumeMySQLSQLResults()
	if !strings.Contains(q.sqlError, "预连接失败") {
		t.Fatalf("preparation failure not visible: %q", q.sqlError)
	}
}
