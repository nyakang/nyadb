package sqlworkspace

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func sqlUITestState(t *testing.T) (*State, *Document, *ui.Tester, int64, *string) {
	t.Helper()
	status := ""
	s := New(Dependencies{
		Connected: func(id int64) bool { return id == 1 },
		Status:    func(message string) { status = message },
	})
	q := s.Open(2, "", 1, "db_test", false)
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c)
		s.Consume()
		s.View(c, q)
	}, 1280, 800)
	t.Cleanup(s.CloseAll)
	tt.Frame()
	return s, q, tt, 1, &status
}

func waitSQLUI(t *testing.T, tt *ui.Tester, query *Document) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for query.Loading() && time.Now().Before(deadline) {
		tt.Frame()
		time.Sleep(5 * time.Millisecond)
	}
	tt.Frame()
	if query.Loading() {
		t.Fatal("SQL UI request did not complete")
	}
}
