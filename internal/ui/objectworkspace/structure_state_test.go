package objectworkspace

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func fakeStructure(database, table string) *mysqlsvc.MySQLTableStructure {
	def := "1"
	return &mysqlsvc.MySQLTableStructure{Database: database, Name: table, Kind: "BASE TABLE", Engine: "InnoDB",
		Columns:     []mysqlsvc.MySQLStructureColumn{{Name: "id", Type: "bigint", DataType: "bigint", Position: 1, Key: "PRI", Default: &def}},
		Indexes:     []mysqlsvc.MySQLStructureIndex{{Name: "PRIMARY", Unique: true, Type: "BTREE", Columns: []mysqlsvc.MySQLStructureIndexColumn{{Name: "id", Position: 1}}}},
		ForeignKeys: []mysqlsvc.MySQLStructureForeignKey{{Name: "fk_order", ReferencedDatabase: database, ReferencedTable: "accounts", OnUpdate: "CASCADE", OnDelete: "RESTRICT", Columns: []mysqlsvc.MySQLStructureForeignKeyColumn{{Name: "account_id", ReferencedName: "id", Position: 1}}}},
	}
}
func waitStructure(t *testing.T, s *State, d *Document) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for d.Loading() && time.Now().Before(deadline) {
		s.Consume(ui.Services{})
		time.Sleep(time.Millisecond)
	}
	s.Consume(ui.Services{})
	if d.Loading() {
		t.Fatal("structure request did not settle")
	}
}
func waitSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal("structure worker did not signal")
	}
}
func testViewer() (*State, *Session) {
	session := &Session{ProfileID: 1, Generation: 7, Connected: true}
	return New(Dependencies{Session: func() Session { return *session }}), session
}

func TestMySQLStructureLateResultCannotReplaceRefreshOrDisconnectedSession(t *testing.T) {
	s, _ := testViewer()
	d := s.OpenStructure(1, 1, "app_db", "orders")
	started, canceled, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{}, 1)
	s.Loader = func(ctx context.Context, _ int64, database, table string) (*mysqlsvc.MySQLTableStructure, error) {
		close(started)
		<-ctx.Done()
		close(canceled)
		<-release
		return fakeStructure(database, "obsolete"), nil
	}
	s.Load(d, func() { done <- struct{}{} }, false)
	waitSignal(t, started)
	s.Loader = func(_ context.Context, _ int64, database, table string) (*mysqlsvc.MySQLTableStructure, error) {
		return fakeStructure(database, table), nil
	}
	s.Load(d, nil, true)
	waitSignal(t, canceled)
	waitStructure(t, s, d)
	close(release)
	waitSignal(t, done)
	s.Consume(ui.Services{})
	if d.Data() == nil || d.Data().Name != "orders" {
		t.Fatal("late old result overwrote refreshed metadata")
	}
	// Closing on disconnect revokes the active read, including a worker that
	// ignores cancellation and returns success after the tab was removed.
	started, canceled, release, done = make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{}, 1)
	s.Loader = func(ctx context.Context, _ int64, db, table string) (*mysqlsvc.MySQLTableStructure, error) {
		close(started)
		<-ctx.Done()
		close(canceled)
		<-release
		return fakeStructure(db, "late_disconnected"), nil
	}
	s.Load(d, func() { done <- struct{}{} }, true)
	waitSignal(t, started)
	s.Close(d)
	waitSignal(t, canceled)
	close(release)
	waitSignal(t, done)
	s.Consume(ui.Services{})
	if d.Loading() || d.results != nil || d.cancel != nil || len(s.documents) != 0 || d.Data() != nil {
		t.Fatal("closed document accepted a late result or kept pending work")
	}
}

func TestMySQLStructureErrorRetryCloseAndCancel(t *testing.T) {
	s, _ := testViewer()
	d := s.OpenStructure(1, 1, "app_db", "orders")
	s.Loader = func(context.Context, int64, string, string) (*mysqlsvc.MySQLTableStructure, error) {
		return nil, errors.New("metadata access denied")
	}
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.Consume(ui.Services{}); s.View(c, d) }, 840, 560)
	s.Load(d, nil, false)
	waitStructure(t, s, d)
	tt.Frame()
	if !strings.Contains(d.Error(), "access denied") || !strings.Contains(strings.Join(tt.Texts(), " "), "access denied") {
		t.Fatal("metadata error not shown")
	}
	var calls atomic.Int32
	s.Loader = func(_ context.Context, _ int64, db, table string) (*mysqlsvc.MySQLTableStructure, error) {
		calls.Add(1)
		return fakeStructure(db, table), nil
	}
	if err := tt.Click("重试读取结构"); err != nil {
		t.Fatal(err)
	}
	waitStructure(t, s, d)
	tt.Frame()
	if d.Data() == nil || d.Error() != "" || !tt.HasText("id") {
		t.Fatal("explicit retry did not recover")
	}
	d.page, d.fieldRow = 2, 0
	s.Load(d, nil, false)
	if calls.Load() != 1 || d.page != 2 {
		t.Fatal("cached document reloaded or lost its subpage")
	}
	started, canceled, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{}, 1)
	s.Loader = func(ctx context.Context, _ int64, db, table string) (*mysqlsvc.MySQLTableStructure, error) {
		close(started)
		<-ctx.Done()
		close(canceled)
		<-release
		return nil, ctx.Err()
	}
	s.Load(d, func() { done <- struct{}{} }, true)
	waitSignal(t, started)
	tt.Frame()
	if err := tt.Click("取消加载"); err != nil {
		t.Fatal(err)
	}
	waitSignal(t, canceled)
	if d.Loading() || d.Data() == nil || d.Error() != "加载已取消" {
		t.Fatal("cancel discarded cache or kept loading")
	}
	close(release)
	waitSignal(t, done)
	s.Consume(ui.Services{})
	if d.Error() != "加载已取消" {
		t.Fatal("late cancellation changed document error")
	}
	s.Close(d)
	s.CloseAll()
	if len(s.documents) != 0 {
		t.Fatal("closed structure document remained registered")
	}
}

func TestStructureParkedResultsRetainProfileAndPresentation(t *testing.T) {
	s, session := testViewer()
	a := s.OpenStructure(1, 1, "shared", "orders")
	b := s.OpenStructure(2, 2, "shared", "orders")
	a.page, a.fieldRow = 1, 0
	a.columns.Columns.Order = []string{"类型", "名称"}
	var calls atomic.Int32
	done := make(chan struct{}, 2)
	s.Loader = func(_ context.Context, id int64, db, table string) (*mysqlsvc.MySQLTableStructure, error) {
		calls.Add(1)
		data := fakeStructure(db, table)
		data.Comment = string(rune('A' + id - 1))
		return data, nil
	}
	s.Load(a, func() { done <- struct{}{} }, false)
	waitSignal(t, done)
	session.ProfileID, session.Generation = 2, 11
	s.Consume(ui.Services{})
	if !a.Loading() || a.Data() != nil {
		t.Fatal("parked profile was consumed under a different session")
	}
	s.Load(b, func() { done <- struct{}{} }, false)
	waitSignal(t, done)
	s.Consume(ui.Services{})
	if b.Data() == nil || b.Data().Comment != "B" {
		t.Fatal("same-named sibling did not receive its own metadata")
	}
	session.ProfileID, session.Generation = 1, 7
	s.Consume(ui.Services{})
	if a.Loading() || a.Data() == nil || a.Data().Comment != "A" || a.page != 1 || len(a.columns.Columns.Order) != 2 {
		t.Fatal("returning profile lost buffered result or presentation state")
	}
	s.Load(a, nil, false)
	if calls.Load() != 2 {
		t.Fatal("returning cached document started another read")
	}
	s.Close(a)
	if b.Data() == nil || b.Data().Comment != "B" || len(s.documents) != 1 {
		t.Fatal("closing one document erased its sibling")
	}
	s.CloseAll()
}

func TestStructureGenerationMismatchSettlesWithoutRetry(t *testing.T) {
	s, session := testViewer()
	d := s.OpenStructure(1, 1, "app_db", "orders")
	var calls atomic.Int32
	done := make(chan struct{}, 1)
	s.Loader = func(_ context.Context, _ int64, db, table string) (*mysqlsvc.MySQLTableStructure, error) {
		calls.Add(1)
		return fakeStructure(db, table), nil
	}
	s.Load(d, func() { done <- struct{}{} }, false)
	waitSignal(t, done)
	session.Generation++
	s.Consume(ui.Services{})
	if d.Loading() || d.Data() != nil || d.results != nil || d.cancel != nil || !strings.Contains(d.Error(), "会话已失效") {
		t.Fatal("obsolete generation stayed loading or accepted metadata")
	}
	s.Load(d, nil, false)
	s.Consume(ui.Services{})
	if calls.Load() != 1 {
		t.Fatal("stale result automatically retried")
	}
	s.Load(d, nil, true)
	waitStructure(t, s, d)
	if calls.Load() != 2 || d.Data() == nil || d.Error() != "" {
		t.Fatal("explicit refresh did not use current generation")
	}
	s.CloseAll()
}

func TestStructureMissingAndCanceledResultsRemainRetryable(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want string
	}{{"empty", nil, "服务器未返回表结构"}, {"canceled", context.Canceled, ""}, {"error", errors.New("permission denied"), "permission denied"}} {
		t.Run(test.name, func(t *testing.T) {
			s, _ := testViewer()
			d := s.OpenStructure(1, 1, "db", "table")
			s.Loader = func(context.Context, int64, string, string) (*mysqlsvc.MySQLTableStructure, error) {
				return nil, test.err
			}
			s.Load(d, nil, false)
			waitStructure(t, s, d)
			if d.Error() != test.want || d.Data() != nil || d.cancel != nil || d.results != nil {
				t.Fatal("failed read retained metadata or pending state")
			}
			s.Loader = func(_ context.Context, _ int64, db, table string) (*mysqlsvc.MySQLTableStructure, error) {
				return fakeStructure(db, table), nil
			}
			s.Load(d, nil, true)
			waitStructure(t, s, d)
			if d.Data() == nil || d.Error() != "" {
				t.Fatal("failed result prevented explicit retry")
			}
			s.CloseAll()
		})
	}
}

func TestStructureClosedDocumentCannotStartAnotherRead(t *testing.T) {
	s, _ := testViewer()
	d := s.OpenStructure(1, 1, "db", "table")
	var calls atomic.Int32
	s.Loader = func(context.Context, int64, string, string) (*mysqlsvc.MySQLTableStructure, error) {
		calls.Add(1)
		return nil, nil
	}
	s.Close(d)
	replacement := s.OpenStructure(1, 1, "db", "replacement")
	s.Load(d, nil, true)
	s.Close(d)
	s.Load(replacement, nil, false)
	waitStructure(t, s, replacement)
	if calls.Load() != 1 || len(s.documents) != 1 || s.documents[1] != replacement {
		t.Fatal("stale document revoked or overwrote replacement")
	}
	s.CloseAll()
}

func TestStructureRejectsUnqualifiedIdentityWithoutReplacingExistingDocument(t *testing.T) {
	s, _ := testViewer()
	original := s.OpenStructure(1, 1, "db", "orders")
	for _, identity := range []struct {
		profile         int64
		database, table string
	}{{0, "db", "orders"}, {1, "", "orders"}, {1, "db", ""}} {
		if s.OpenStructure(1, identity.profile, identity.database, identity.table) != nil {
			t.Fatal("unqualified identity was admitted")
		}
		if s.documents[1] != original || original.closed {
			t.Fatal("invalid open revoked an existing document")
		}
	}
	s.CloseAll()
}
