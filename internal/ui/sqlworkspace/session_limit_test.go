package sqlworkspace

import (
	"context"
	"errors"
	"strings"
	"testing"

	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

func TestSQLTabLimitErrorVisibleAndRetryableInWorkspace(t *testing.T) {
	s, q, tt, _, status := sqlUITestState(t)
	q.Editor.Buffer.Set("SELECT 1")
	s.Executor = func(context.Context, int64, string, string) (mysqlsvc.MySQLSQLResult, error) {
		return mysqlsvc.MySQLSQLResult{}, mysqlsvc.ErrMySQLSQLSessionLimit
	}
	if err := tt.Click("运行当前或选中 SQL"); err != nil {
		t.Fatal(err)
	}
	waitSQLUI(t, tt, q)
	if !strings.Contains(q.Error(), "关闭其他查询标签后重试") ||
		!strings.Contains(*status, "SQL 执行失败") {
		t.Fatalf("quota error not reported: %q, %q", q.Error(), *status)
	}
	if q.Editor.Buffer.String() != "SELECT 1" {
		t.Fatal("quota rejection discarded SQL editor text")
	}
	s.Executor = func(context.Context, int64, string, string) (mysqlsvc.MySQLSQLResult, error) {
		return mysqlsvc.MySQLSQLResult{Columns: []string{"id"}, Rows: [][]string{{"1"}}}, nil
	}
	if err := tt.Click("运行当前或选中 SQL"); err != nil {
		t.Fatal(err)
	}
	waitSQLUI(t, tt, q)
	if q.Error() != "" || q.Result() == nil || q.Result().Rows[0][0] != "1" {
		t.Fatalf("tab did not recover after budget became available: %q, %+v", q.Error(), q.Result())
	}
}

func TestSQLTabLimitPreparationErrorRemainsDistinctFromLostSession(t *testing.T) {
	s := New(Dependencies{})
	q := s.Open(2, "", 1, "db_test", false)
	done := make(chan error, 1)
	done <- errors.New("limit reached")
	q.prepareResults = done
	s.Consume()
	if !strings.Contains(q.Error(), "预连接失败") {
		t.Fatalf("preparation failure not visible: %q", q.Error())
	}
}
