package objectworkspace

import (
	"context"
	"errors"
	"fmt"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
	"sync/atomic"
	"testing"
)

func viewCommit(_ context.Context, r contract.ViewDesignRequest) contract.ViewDesignOutcome {
	a := r.Original.Clone()
	a.Schema.Exists = r.Action != "drop"
	if r.Action != "drop" {
		a.Target.Object.Name = r.Desired.Name
		a.Schema.View = r.Desired.Clone()
	}
	return contract.ViewDesignOutcome{State: "committed", Verified: true, Actual: &a}
}
func viewTestState(t *testing.T, action string, apply func(context.Context, contract.ViewDesignRequest) contract.ViewDesignOutcome) (*State, *Session, *uint64, *ui.Tester) {
	t.Helper()
	w := &Session{ProfileID: 83, Generation: 9, Connected: true}
	published := uint64(73)
	s := New(Dependencies{Session: func() Session { return *w }, SessionFor: func(int64) Session { return *w }, Browser: func() BrowserSnapshot {
		return BrowserSnapshot{ProfileID: w.ProfileID, Database: "aux", Connected: w.Connected, Opened: true}
	}, Capabilities: func(int64) contract.Capabilities { return contract.Capabilities{} },
		ViewTarget: func(id int64, database, name string) contract.SchemaDesignTarget {
			return contract.SchemaDesignTarget{Object: contract.ObjectRef{ProfileID: id, Catalog: "main", Schema: database, Kind: "view", Name: name}, Generation: published}
		},
		ViewDesignFor: func(int64) contract.ViewDesignOperations {
			return contract.ViewDesignOperations{CanApply: func(t contract.SchemaDesignTarget) bool { return t.Generation == published }, Snapshot: func(_ context.Context, target contract.SchemaDesignTarget) (contract.ViewDesignSnapshot, error) {
				v := schemaddl.View{Schema: target.Object.Schema, Name: target.Object.Name, SelectSQL: "SELECT 1 AS id"}
				ddl, err := schemaddl.CreateSQLiteView(v, target.Object.Schema)
				return contract.ViewDesignSnapshot{Target: target, Schema: schemaddl.ViewSnapshot{Engine: "sqlite", Revision: "original", Exists: action != "create", DDL: ddl, View: v}}, err
			}, Apply: apply}
		},
	})
	s.BeginView(ui.Services{}, 83, "aux", "v", action)
	tt := tableModalTester(s)
	waitSchema(t, tt, func() bool { return !s.ViewPending() })
	t.Cleanup(s.CloseAll)
	return s, w, &published, tt
}
func previewView(t *testing.T, tt *ui.Tester) {
	t.Helper()
	if err := tt.Click("SQL 预览"); err != nil {
		t.Fatal(err)
	}
}
func confirmView(t *testing.T, tt *ui.Tester) {
	t.Helper()
	if err := tt.Click("确认执行视图 SQL"); err != nil {
		t.Fatal(err)
	}
}

func TestViewNativePreviewGuardImmutableRequestAndOldPreparedInput(t *testing.T) {
	entered, ack := make(chan contract.ViewDesignRequest, 1), make(chan struct{})
	s, _, _, tt := viewTestState(t, "alter", func(ctx context.Context, r contract.ViewDesignRequest) contract.ViewDesignOutcome {
		entered <- r
		<-ack
		return viewCommit(ctx, r)
	})
	s.viewOp.text = `CREATE VIEW "aux"."v" AS SELECT 2 AS id`
	previewView(t, tt)
	s.viewOp.text = `CREATE VIEW "aux"."v" AS SELECT 3 AS id`
	s.applyView(ui.Services{})
	if s.ViewPending() {
		t.Fatal("old preview executed changed input")
	}
	s.viewOp.text = `CREATE VIEW "aux"."v" AS SELECT 2 AS id`
	var next func(ui.Services)
	s.deps.BeforeDDL = func(_ ui.Services, id int64, db, name string, f func(ui.Services)) bool {
		if id != 83 || db != "aux" || name != "v" {
			t.Fatal("guard retargeted")
		}
		next = f
		return true
	}
	confirmView(t, tt)
	if next == nil || s.ViewPending() {
		t.Fatal("guard bypassed")
	}
	next(ui.Services{})
	r := <-entered
	if r.Original.Target.Object.ProfileID != 83 || r.Original.Target.Object.Schema != "aux" || r.Desired.SelectSQL != "SELECT 2 AS id" {
		t.Fatal("request lost original scope")
	}
	s.viewOp.text, s.viewOp.name = "presentation changed", "another"
	var got *ViewEvent
	s.deps.ViewChanged = func(_ ui.Services, event ViewEvent) { got = &event }
	close(ack)
	waitSchema(t, tt, func() bool { return !s.ViewPending() })
	if got == nil || got.Request.Desired.SelectSQL != "SELECT 2 AS id" || got.Request.Desired.Name != "v" || s.ViewOpen() {
		t.Fatal("receipt read mutable input")
	}
}

func TestViewCancellationWaitsForAcknowledgementAndPreservesDraft(t *testing.T) {
	entered, ack := make(chan struct{}), make(chan struct{})
	s, _, _, tt := viewTestState(t, "alter", func(ctx context.Context, _ contract.ViewDesignRequest) contract.ViewDesignOutcome {
		close(entered)
		<-ctx.Done()
		<-ack
		return contract.ViewDesignOutcome{State: "rolled_back", Error: ctx.Err()}
	})
	s.viewOp.text = "CREATE VIEW v AS SELECT 2 AS id"
	previewView(t, tt)
	confirmView(t, tt)
	<-entered
	if err := tt.Click("取消操作"); err != nil {
		t.Fatal(err)
	}
	if !s.ViewPending() || !s.nativeTablePending() || !s.viewOp.cancelRequested {
		t.Fatal("cancel released ownership before ack")
	}
	close(ack)
	waitSchema(t, tt, func() bool { return !s.ViewPending() })
	if !s.viewOp.blocked || s.viewOp.cancelRequested || s.viewOp.text != "CREATE VIEW v AS SELECT 2 AS id" {
		t.Fatal("cancel discarded draft or allowed replay")
	}
	if err := tt.Click("重新读取原定义"); err != nil {
		t.Fatal(err)
	}
	waitSchema(t, tt, func() bool { return !s.ViewPending() })
	if s.viewOp.blocked || s.viewOp.text != "CREATE VIEW v AS SELECT 2 AS id" || s.viewOp.prepared != nil {
		t.Fatal("explicit reread lost draft or retained old preview")
	}
}

func TestViewCancelledGuardAndClosedContinuationCannotLaunch(t *testing.T) {
	var calls atomic.Int32
	s, _, _, tt := viewTestState(t, "alter", func(ctx context.Context, r contract.ViewDesignRequest) contract.ViewDesignOutcome {
		calls.Add(1)
		return viewCommit(ctx, r)
	})
	pending := true
	var next func(ui.Services)
	s.deps.TableGuardPending = func(int64, string, string) bool { return pending }
	s.deps.BeforeDDL = func(_ ui.Services, _ int64, _, _ string, f func(ui.Services)) bool { next = f; return true }
	previewView(t, tt)
	confirmView(t, tt)
	tt.Frame()
	if s.ViewOpen() {
		t.Fatal("view preview covered draft decision")
	}
	pending = false
	tt.Frame()
	next(ui.Services{})
	if !s.ViewOpen() || calls.Load() != 0 || s.ViewPending() {
		t.Fatal("cancelled guard launched request")
	}
	confirmView(t, tt)
	s.closeView()
	s.BeginView(ui.Services{}, 83, "aux", "replacement", "alter")
	next(ui.Services{})
	if calls.Load() != 0 || s.viewOp.target.Object.Name != "replacement" {
		t.Fatal("closed guard launched into new modal")
	}
}

func TestViewParkedAndClosedReceiptsKeepOriginalIdentity(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		t.Run(fmt.Sprint(terminal), func(t *testing.T) {
			s, w, _, _ := viewTestState(t, "alter", viewCommit)
			x := s.viewOp
			r, _, err := s.viewRequest(x)
			if err != nil {
				t.Fatal(err)
			}
			x.applied = &r
			out := viewCommit(context.Background(), r)
			ch := make(chan viewReply, 1)
			x.results, x.busy, x.applying = ch, true, true
			var got *ViewEvent
			s.deps.ViewChanged = func(_ ui.Services, event ViewEvent) { got = &event }
			w.ProfileID = 99
			if terminal {
				s.CloseAll()
			}
			ch <- viewReply{outcome: &out}
			s.Consume(ui.Services{})
			if terminal {
				if got != nil {
					t.Fatal("closed host received mutation")
				}
			} else if got == nil || got.Request.Original.Target != r.Original.Target || got.Generation != 9 || s.viewOp != nil {
				t.Fatal("parked receipt lost original scope")
			}
		})
	}
}

func TestViewDisconnectPreservesDraftAndResumeRequiresOriginalConnection(t *testing.T) {
	s, w, published, tt := viewTestState(t, "alter", viewCommit)
	s.viewOp.text = "CREATE VIEW v AS SELECT 'preserved' AS id"
	w.ProfileID = 99
	tt.Frame()
	if s.viewOp != nil || s.parkedViewOp == nil {
		t.Fatal("profile switch discarded view draft")
	}
	s.resumeView()
	if s.viewOp != nil {
		t.Fatal("resume acted on another profile")
	}
	w.ProfileID = 83
	w.Connected = false
	if s.canResumeView() || s.browserActions().canResume {
		t.Fatal("disconnected view draft remained enabled")
	}
	s.resumeView()
	if s.viewOp != nil || s.parkedViewOp.text != "CREATE VIEW v AS SELECT 'preserved' AS id" {
		t.Fatal("disconnected resume changed draft")
	}
	w.Connected = true
	s.tablePending = true
	if s.canResumeView() || s.browserActions().canResume {
		t.Fatal("view resume competed with a pending table operation")
	}
	s.tablePending = false
	if !s.canResumeView() || !s.browserActions().canResume {
		t.Fatal("original connected view draft was not available")
	}
	w.Generation++
	*published++
	s.resumeView()
	tt.Frame()
	if !s.ViewOpen() || s.viewOp.original != nil || s.viewOp.prepared != nil || s.viewOp.text != "CREATE VIEW v AS SELECT 'preserved' AS id" {
		t.Fatal("explicit recovery replayed or lost draft")
	}
}

func TestViewDropNameConfirmationAndFailureKeepsOriginalDefinition(t *testing.T) {
	var calls atomic.Int32
	s, _, _, tt := viewTestState(t, "drop", func(context.Context, contract.ViewDesignRequest) contract.ViewDesignOutcome {
		calls.Add(1)
		return contract.ViewDesignOutcome{State: "rolled_back", Error: errors.New("dependent view would be invalid")}
	})
	previewView(t, tt)
	s.viewOp.confirmation = "wrong"
	s.applyView(ui.Services{})
	if calls.Load() != 0 || s.ViewPending() {
		t.Fatal("wrong name deleted view")
	}
	s.viewOp.confirmation = "v"
	tt.Frame()
	confirmView(t, tt)
	waitSchema(t, tt, func() bool { return !s.ViewPending() })
	if !s.viewOp.blocked || s.viewOp.confirmation != "v" || s.viewOp.text == "" || !tt.HasText("dependent view would be invalid") {
		t.Fatal("failed drop lost original confirmation")
	}
}

func TestViewContextMenuRejectsOldPublishedGenerationAndReadOnlyBinding(t *testing.T) {
	s, _, published, _ := viewTestState(t, "alter", viewCommit)
	s.closeView()
	tt := ui.NewTester(func(c *ui.Context) {
		s.Consume(c.Services())
		ui.Button(c, "View menu").Menu(func(m *ui.Menu) { s.ContextMenu(c.Services(), m, 83, "aux", "v", "视图") })
		s.Modals(c)
	}, 900, 700)
	tt.Frame()
	if err := tt.Click("View menu"); err != nil {
		t.Fatal(err)
	}
	*published++
	if err := tt.ChooseMenuItem("编辑完整定义…"); err != nil {
		t.Fatal(err)
	}
	if s.ViewOpen() {
		t.Fatal("old published menu generation acted on new session")
	}
	tt.Frame()
	if err := tt.Click("View menu"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("重命名视图…"); err != nil {
		t.Fatal(err)
	}
	if s.viewOp == nil || s.viewOp.action != "rename" {
		t.Fatal("view menu not wired")
	}
	waitSchema(t, tt, func() bool { return !s.ViewPending() })
	s.closeView()
	s.deps.ViewDesignFor = func(int64) contract.ViewDesignOperations {
		return contract.ViewDesignOperations{Snapshot: func(context.Context, contract.SchemaDesignTarget) (contract.ViewDesignSnapshot, error) {
			return contract.ViewDesignSnapshot{}, nil
		}, CanApply: func(contract.SchemaDesignTarget) bool { return false }, Apply: viewCommit}
	}
	tt.Frame()
	if err := tt.Click("View menu"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("删除对象…"); err == nil {
		t.Fatal("read-only binding enabled delete")
	}
}
