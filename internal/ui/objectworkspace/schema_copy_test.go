package objectworkspace

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

func copyCommit(_ context.Context, r contract.SchemaCopyRequest) contract.SchemaDesignOutcome {
	a := r.Original.Clone()
	a.Target.Object.Name, a.Schema.Table.Name = r.Destination, r.Destination
	return contract.SchemaDesignOutcome{State: "committed", Verified: true, Actual: &a}
}
func copyTestState(t *testing.T, run func(context.Context, contract.SchemaCopyRequest) contract.SchemaDesignOutcome) (*State, *Session, *ui.Tester) {
	t.Helper()
	s, w, _ := dropTestState(t, dropCommit)
	s.closeSchemaDrop()
	original := s.deps.SchemaDesignFor
	s.deps.SchemaDesignFor = func(id int64) contract.SchemaDesignOperations {
		ops := original(id)
		load := ops.Snapshot
		ops.Snapshot = func(ctx context.Context, target contract.SchemaDesignTarget) (contract.SchemaDesignSnapshot, error) {
			a, err := load(ctx, target)
			a.Schema.Table, _ = schemaddl.ParseSQLite("CREATE TABLE t(id INTEGER PRIMARY KEY,value TEXT)")
			return a, err
		}
		ops.CanCopy = func(target contract.SchemaDesignTarget) bool { return target.Object.ProfileID == 83 }
		ops.Copy = run
		return ops
	}
	s.BeginTableChange(83, "main", "t", mysqlsvc.MySQLTableCopyData)
	tt := tableModalTester(s)
	tt.Frame()
	if err := tt.Click("重新读取结构"); err != nil {
		t.Fatal(err)
	}
	waitSchema(t, tt, func() bool { return !s.TablePending() })
	return s, w, tt
}

func TestSchemaCopyPreviewGuardAndImmutableDestinationChoices(t *testing.T) {
	entered, ack := make(chan contract.SchemaCopyRequest, 1), make(chan struct{})
	s, _, tt := copyTestState(t, func(ctx context.Context, r contract.SchemaCopyRequest) contract.SchemaDesignOutcome {
		entered <- r
		<-ack
		return copyCommit(ctx, r)
	})
	if !tt.HasText("复制结构 SQL 预览") || s.schemaCopy.original == nil {
		t.Fatal("native preview missing")
	}
	var next func(ui.Services)
	s.deps.BeforeCopy = func(_ ui.Services, id int64, db, table string, f func(ui.Services)) bool {
		if id != 83 || db != "main" || table != "t" {
			t.Fatal("guard retargeted")
		}
		next = f
		return true
	}
	if err := tt.Click("确认复制表"); err != nil {
		t.Fatal(err)
	}
	if next == nil || s.TablePending() {
		t.Fatal("draft guard bypassed")
	}
	next(ui.Services{})
	r := <-entered
	if r.Destination != "t_copy" || !r.WithData || !r.RebindSelfReferences || !r.PreserveRowID {
		t.Fatal("copy choices lost")
	}
	x := s.schemaCopy
	x.destination, x.withData = "another", false
	x.original.Schema.Table.Clauses[0].Name = "presentation changed"
	var got *SchemaCopyEvent
	s.deps.SchemaCopied = func(_ ui.Services, event SchemaCopyEvent) { got = &event }
	close(ack)
	waitSchema(t, tt, func() bool { return !s.TablePending() })
	if got == nil || got.Destination != "t_copy" || got.Original != r.Original.Target || s.schemaCopy != nil {
		t.Fatal("receipt read mutable destination or failed to close")
	}
}

func TestSchemaCopyCancelledGuardRestoresPreviewAndRevokesOldContinuation(t *testing.T) {
	var calls atomic.Int32
	s, _, tt := copyTestState(t, func(ctx context.Context, r contract.SchemaCopyRequest) contract.SchemaDesignOutcome {
		calls.Add(1)
		return copyCommit(ctx, r)
	})
	pending := true
	var next func(ui.Services)
	s.deps.BeforeCopy = func(_ ui.Services, _ int64, _, _ string, f func(ui.Services)) bool { next = f; return true }
	s.deps.TableGuardPending = func(int64, string, string) bool { return pending }
	if err := tt.Click("确认复制表"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if s.TableDialogOpen() {
		t.Fatal("preview covered document decision")
	}
	pending = false
	tt.Frame()
	if !s.TableDialogOpen() {
		t.Fatal("cancelled document decision left preview hidden")
	}
	next(ui.Services{})
	if calls.Load() != 0 || s.TablePending() {
		t.Fatal("cancelled decision launched late copy")
	}
}

func TestSchemaCopyClosedOrChangedGuardCannotLaunch(t *testing.T) {
	var calls atomic.Int32
	s, _, tt := copyTestState(t, func(ctx context.Context, r contract.SchemaCopyRequest) contract.SchemaDesignOutcome {
		calls.Add(1)
		return copyCommit(ctx, r)
	})
	var next func(ui.Services)
	s.deps.BeforeCopy = func(_ ui.Services, _ int64, _, _ string, f func(ui.Services)) bool { next = f; return true }
	if err := tt.Click("确认复制表"); err != nil {
		t.Fatal(err)
	}
	s.schemaCopy.destination = "new_choice"
	next(ui.Services{})
	if calls.Load() != 0 {
		t.Fatal("changed choices launched old request")
	}
	s.closeSchemaCopy()
	s.beginSchemaCopy(83, "main", "replacement", false)
	next(ui.Services{})
	if calls.Load() != 0 || s.schemaCopy.target.Object.Name != "replacement" {
		t.Fatal("closed guard acted on replacement")
	}
}

func TestSchemaCopyCancellationRetainsOwnershipUntilAcknowledgement(t *testing.T) {
	entered, ack := make(chan struct{}), make(chan struct{})
	s, _, tt := copyTestState(t, func(ctx context.Context, _ contract.SchemaCopyRequest) contract.SchemaDesignOutcome {
		close(entered)
		<-ctx.Done()
		<-ack
		return contract.SchemaDesignOutcome{State: "rolled_back", Error: ctx.Err()}
	})
	if err := tt.Click("确认复制表"); err != nil {
		t.Fatal(err)
	}
	<-entered
	if err := tt.Click("取消"); err != nil {
		t.Fatal(err)
	}
	if !s.TablePending() || !s.schemaCopy.cancelRequested || s.CanDropTable(83, "main", "t") || s.CanCopyTable(83, "main", "t") {
		t.Fatal("cancel released mutation scope early")
	}
	s.BeginTableChange(83, "main", "other", mysqlsvc.MySQLTableDrop)
	if s.schemaCopy == nil || s.schemaDrop != nil {
		t.Fatal("copy replaced before ack")
	}
	close(ack)
	waitSchema(t, tt, func() bool { return !s.TablePending() })
	if !s.schemaCopy.blocked || s.schemaCopy.cancelRequested || s.TableError() == "" {
		t.Fatal("cancel allowed silent replay")
	}
	if err := tt.Click("重新读取结构"); err != nil {
		t.Fatal(err)
	}
	waitSchema(t, tt, func() bool { return !s.TablePending() })
	if s.schemaCopy.blocked {
		t.Fatal("explicit reread kept blocked request")
	}
}

func TestSchemaCopyParkedAndClosedReceiptsKeepOriginalScope(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		t.Run(map[bool]string{false: "parked", true: "closed"}[terminal], func(t *testing.T) {
			s, w, _ := copyTestState(t, copyCommit)
			var got *SchemaCopyEvent
			s.deps.SchemaCopied = func(_ ui.Services, event SchemaCopyEvent) { got = &event }
			x := s.schemaCopy
			r, _, err := s.schemaCopyRequest(x)
			if err != nil {
				t.Fatal(err)
			}
			x.applied = &r
			out := copyCommit(context.Background(), r)
			ch := make(chan schemaCopyReply, 1)
			x.results, x.busy, x.applying = ch, true, true
			w.ProfileID = 99
			if terminal {
				s.CloseAll()
			}
			ch <- schemaCopyReply{outcome: &out}
			s.Consume(ui.Services{})
			if terminal {
				if got != nil {
					t.Fatal("closed host received copy")
				}
			} else if got == nil || got.Original != x.target || got.Generation != 9 || s.schemaCopy != nil {
				t.Fatal("parked receipt lost original scope")
			}
		})
	}
}

func TestSchemaCopyFailuresAndHostReplacementDoNotLosePresentation(t *testing.T) {
	s, _, tt := copyTestState(t, func(context.Context, contract.SchemaCopyRequest) contract.SchemaDesignOutcome {
		return contract.SchemaDesignOutcome{State: "rolled_back", Error: errors.New("destination exists")}
	})
	if err := tt.Click("确认复制表"); err != nil {
		t.Fatal(err)
	}
	waitSchema(t, tt, func() bool { return !s.TablePending() })
	if s.schemaCopy.destination != "t_copy" || !s.schemaCopy.withData || !s.schemaCopy.blocked || !tt.HasText("destination exists") {
		t.Fatal("failed copy discarded original choices")
	}
	s.closeSchemaCopy()
	s, _, tt = copyTestState(t, copyCommit)
	s.deps.SchemaCopied = func(_ ui.Services, _ SchemaCopyEvent) { s.beginSchemaCopy(83, "main", "replacement", false) }
	if err := tt.Click("确认复制表"); err != nil {
		t.Fatal(err)
	}
	waitSchema(t, tt, func() bool { return !s.TablePending() })
	if s.schemaCopy == nil || s.schemaCopy.target.Object.Name != "replacement" || !s.schemaCopy.open {
		t.Fatal("old receipt closed replacement modal")
	}
}

func TestSchemaCopyClosedReadReceiptCannotPopulateNewScope(t *testing.T) {
	s, _, _ := copyTestState(t, copyCommit)
	old := s.schemaCopy
	ch := make(chan schemaCopyReply, 1)
	old.results, old.busy = ch, true
	s.closeSchemaCopy()
	s.beginSchemaCopy(83, "main", "replacement", false)
	ch <- schemaCopyReply{snapshot: old.original.Clone()}
	s.Consume(ui.Services{})
	if s.schemaCopy.original != nil || s.schemaCopy.err != "" || s.schemaCopy.target.Object.Name != "replacement" {
		t.Fatal("closed read populated replacement")
	}
}

func TestSchemaCopyMenuUsesOptionalBindingAndOpeningGeneration(t *testing.T) {
	s, w, _ := copyTestState(t, copyCommit)
	s.closeSchemaCopy()
	s.deps.Browser = func() BrowserSnapshot {
		return BrowserSnapshot{ProfileID: 83, Database: "main", Connected: true, Opened: true}
	}
	s.deps.SessionFor = func(int64) Session { return *w }
	tt := ui.NewTester(func(c *ui.Context) {
		s.Consume(c.Services())
		ui.Button(c, "Table menu").Menu(func(m *ui.Menu) { s.ContextMenu(c.Services(), m, 83, "main", "t", "表") })
		s.Modals(c)
	}, 900, 700)
	for _, data := range []bool{false, true} {
		tt.Frame()
		if err := tt.Click("Table menu"); err != nil {
			t.Fatal(err)
		}
		label := "仅结构"
		if data {
			label = "结构和数据"
		}
		if err := tt.ChooseMenuItem("复制表", label); err != nil {
			t.Fatal(err)
		}
		if s.schemaCopy == nil || s.schemaCopy.withData != data || s.tableDialog {
			t.Fatal("menu dispatched unbound MySQL action")
		}
		s.closeSchemaCopy()
	}
	tt.Frame()
	if err := tt.Click("Table menu"); err != nil {
		t.Fatal(err)
	}
	w.Generation++
	if err := tt.ChooseMenuItem("复制表", "仅结构"); err != nil {
		t.Fatal(err)
	}
	if s.schemaCopy != nil {
		t.Fatal("old menu targeted replacement session")
	}
	s.deps.SchemaDesignFor = func(int64) contract.SchemaDesignOperations {
		return contract.SchemaDesignOperations{CanCopy: func(contract.SchemaDesignTarget) bool { return false }, Copy: copyCommit}
	}
	tt.Frame()
	if err := tt.Click("Table menu"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("复制表", "仅结构"); err == nil {
		t.Fatal("incomplete/read-only copy binding enabled")
	}
}
