package objectworkspace

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/mysqlddl"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

func designTestState(t *testing.T) (*State, *Document, *Session, *ui.Tester) {
	t.Helper()
	session := &Session{ProfileID: 1, Generation: 7, Connected: true}
	s := New(Dependencies{Session: func() Session { return *session }, SessionFor: func(id int64) Session { return Session{ProfileID: id, Generation: 7, Connected: true} }, DesignLoader: func() DesignLoader {
		return func(_ context.Context, id int64, db, table string) (mysqlddl.Snapshot, error) {
			return mysqlddl.Snapshot{Database: db, Table: table, DDL: "CREATE TABLE `t` (`id` bigint NOT NULL,`value` text DEFAULT NULL) ENGINE=InnoDB", RuntimeGeneration: 11}, nil
		}
	}})
	d := s.OpenStructure(2, 1, "db", "t")
	tt := ui.NewTester(func(c *ui.Context) { s.Consume(c.Services()); s.View(c, d) }, 1280, 800)
	t.Cleanup(s.CloseAll)
	s.BeginDesign(d, ui.Services{})
	waitDesign(t, s, d, tt)
	return s, d, session, tt
}
func waitDesign(t *testing.T, s *State, d *Document, tt *ui.Tester) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for d.DesignBusy() && time.Now().Before(deadline) {
		tt.Frame()
		time.Sleep(time.Millisecond)
	}
	tt.Frame()
	if d.DesignBusy() {
		t.Fatal("design request did not settle")
	}
}
func TestTableDesignerPreviewConfirmationAndSuccessfulSave(t *testing.T) {
	s, d, _, tt := designTestState(t)
	calls := atomic.Int32{}
	s.deps.DesignExecutor = func() DesignExecutor {
		return func(_ context.Context, id int64, plan mysqlddl.Plan) mysqlsvc.DDLOutcome {
			calls.Add(1)
			if id != 1 || plan.Original.RuntimeGeneration != 11 || len(plan.Statements) != 1 || !strings.Contains(plan.Statements[0], "ADD COLUMN") {
				return mysqlsvc.DDLOutcome{State: "not_executed", Error: errors.New("wrong plan owner")}
			}
			actual := plan.Original
			actual.DDL = plan.Desired.Table.DDL()
			return mysqlsvc.DDLOutcome{State: "executed", Verified: true, Actual: &actual}
		}
	}
	if e := tt.Click("添加字段"); e != nil {
		t.Fatal(e)
	}
	if !d.DesignDirty() {
		t.Fatal("adding field did not mark design dirty")
	}
	if e := tt.Click("SQL 预览"); e != nil {
		t.Fatal(e)
	}
	if calls.Load() != 0 || !d.design.preview {
		t.Fatal("preview executed DDL")
	}
	if e := tt.Click("确认执行结构 SQL"); e != nil {
		t.Fatal(e)
	}
	waitDesign(t, s, d, tt)
	if calls.Load() != 1 || d.DesignDirty() || d.err != "" {
		t.Fatalf("save did not settle: calls=%d dirty=%t %s", calls.Load(), d.DesignDirty(), d.err)
	}
}
func TestTableDesignerCancelAndPartialOutcomesRetainDraftWithoutReplay(t *testing.T) {
	s, d, _, tt := designTestState(t)
	started, release := make(chan struct{}), make(chan struct{})
	calls := atomic.Int32{}
	s.deps.DesignExecutor = func() DesignExecutor {
		return func(ctx context.Context, _ int64, _ mysqlddl.Plan) mysqlsvc.DDLOutcome {
			calls.Add(1)
			close(started)
			<-ctx.Done()
			<-release
			return mysqlsvc.DDLOutcome{State: "uncertain", Error: ctx.Err()}
		}
	}
	s.addDesignClause(d, mysqlddl.Column)
	if e := s.PrepareDesign(d); e != nil {
		t.Fatal(e)
	}
	s.executeDesign(d, ui.Services{})
	waitSignal(t, started)
	s.cancelDesign(d)
	s.executeDesign(d, ui.Services{})
	if !d.DesignBusy() || calls.Load() != 1 {
		t.Fatal("cancel replayed or settled without executor acknowledgement")
	}
	close(release)
	waitDesign(t, s, d, tt)
	if !d.DesignDirty() || !d.design.blocked || s.PrepareDesign(d) == nil || calls.Load() != 1 {
		t.Fatal("uncertain DDL discarded draft or allowed replay")
	}
}
func TestTableDesignerParkedCompletionAndGenerationInvalidation(t *testing.T) {
	s, d, w, tt := designTestState(t)
	started, release := make(chan struct{}), make(chan struct{})
	events := make(chan DesignEvent, 1)
	s.deps.Designed = func(_ ui.Services, event DesignEvent) { events <- event }
	s.deps.DesignExecutor = func() DesignExecutor {
		return func(_ context.Context, _ int64, _ mysqlddl.Plan) mysqlsvc.DDLOutcome {
			close(started)
			<-release
			return mysqlsvc.DDLOutcome{State: "partial", Error: errors.New("second statement rejected")}
		}
	}
	s.addDesignClause(d, mysqlddl.Column)
	_ = s.PrepareDesign(d)
	s.executeDesign(d, ui.Services{})
	waitSignal(t, started)
	w.ProfileID = 2
	close(release)
	tt.Frame()
	if !d.DesignBusy() {
		t.Fatal("parked result applied to another profile")
	}
	w.ProfileID = 1
	w.Generation++
	waitDesign(t, s, d, tt)
	event := <-events
	if event.ProfileID != 1 || event.Generation != 7 || event.Database != "db" || event.Table != "t" || !d.DesignDirty() || !d.design.blocked {
		t.Fatal("completion lost original identity or draft")
	}
}
func TestTableDesignerDiscardRestoresOriginalWhenNextGuardCancels(t *testing.T) {
	s, d, _, tt := designTestState(t)
	original := d.design.baseline
	s.addDesignClause(d, mysqlddl.Column)
	if !s.GuardDesignLeave(d, func(ui.Services) {}) {
		t.Fatal("dirty design close not guarded")
	}
	tt.Frame()
	if e := tt.Click("放弃"); e != nil {
		t.Fatal(e)
	}
	if d.DesignDirty() || designSignature(d.design.draft) != original {
		t.Fatal("discard falsely saved edited structure")
	}
}
