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

func dropTestState(t *testing.T, apply func(context.Context, contract.SchemaDesignSnapshot) contract.SchemaDesignOutcome) (*State, *Session, *ui.Tester) {
	t.Helper()
	w := &Session{ProfileID: 83, Generation: 9, Connected: true}
	var s *State
	s = New(Dependencies{Session: func() Session { return *w }, SessionFor: func(id int64) Session {
		if id == 83 {
			return Session{ProfileID: 83, Generation: 9, Connected: true}
		}
		return *w
	},
		SchemaTarget: func(id int64, db, name string) contract.SchemaDesignTarget {
			return contract.SchemaDesignTarget{Object: contract.ObjectRef{ProfileID: id, Catalog: "main", Schema: db, Kind: "table", Name: name}, Generation: 73}
		},
		SchemaDesignFor: func(int64) contract.SchemaDesignOperations {
			return contract.SchemaDesignOperations{
				CanDrop: func(target contract.SchemaDesignTarget) bool { return target.Object.ProfileID == 83 },
				Snapshot: func(_ context.Context, target contract.SchemaDesignTarget) (contract.SchemaDesignSnapshot, error) {
					return contract.SchemaDesignSnapshot{Target: target, Schema: schemaddl.Snapshot{Engine: "sqlite", Revision: "original", Exists: true, Table: schemaddl.Table{Name: target.Object.Name, Clauses: []schemaddl.Clause{{Name: "id"}}}}}, nil
				}, Drop: apply}
		},
	})
	s.BeginTableChange(83, "main", "t", mysqlsvc.MySQLTableDrop)
	tt := tableModalTester(s)
	tt.Frame()
	if err := tt.Click("重新读取结构"); err != nil {
		t.Fatal(err)
	}
	waitSchema(t, tt, func() bool { return !s.TablePending() })
	t.Cleanup(s.CloseAll)
	return s, w, tt
}
func confirmDrop(t *testing.T, tt *ui.Tester) {
	t.Helper()
	if err := tt.Click("确认原表名称"); err != nil {
		t.Fatal(err)
	}
	tt.Type("t")
	if err := tt.Click("确认删除表"); err != nil {
		t.Fatal(err)
	}
}
func dropCommit(_ context.Context, r contract.SchemaDesignSnapshot) contract.SchemaDesignOutcome {
	r.Schema.Exists = false
	return contract.SchemaDesignOutcome{State: "committed", Verified: true, Actual: &r}
}

func TestSchemaDropNativePreviewGuardAndImmutableOriginalRequest(t *testing.T) {
	var calls atomic.Int32
	s, _, tt := dropTestState(t, func(ctx context.Context, r contract.SchemaDesignSnapshot) contract.SchemaDesignOutcome {
		calls.Add(1)
		return dropCommit(ctx, r)
	})
	if calls.Load() != 0 || !tt.HasText("删除 SQL 预览") || s.schemaDrop.original.Target.Object.Name != "t" {
		t.Fatal("preview executed or scope missing")
	}
	var continuation func(ui.Services)
	s.deps.BeforeDrop = func(_ ui.Services, id int64, db, table string, next func(ui.Services)) bool {
		if id != 83 || db != "main" || table != "t" {
			t.Fatal("guard retargeted")
		}
		continuation = next
		return true
	}
	confirmDrop(t, tt)
	if calls.Load() != 0 || continuation == nil {
		t.Fatal("draft guard bypassed")
	}
	continuation(ui.Services{})
	s.schemaDrop.original.Schema.Table.Clauses[0].Name = "UI changed after launch"
	waitSchema(t, tt, func() bool { return !s.TablePending() })
	if calls.Load() != 1 || s.TableDialogOpen() {
		t.Fatal("verified drop did not settle")
	}
}

func TestSchemaDropClosedGuardAndReadReceiptCannotActOnReplacement(t *testing.T) {
	var calls atomic.Int32
	s, _, tt := dropTestState(t, func(ctx context.Context, r contract.SchemaDesignSnapshot) contract.SchemaDesignOutcome {
		calls.Add(1)
		return dropCommit(ctx, r)
	})
	var next func(ui.Services)
	s.deps.BeforeDrop = func(_ ui.Services, _ int64, _, _ string, f func(ui.Services)) bool { next = f; return true }
	confirmDrop(t, tt)
	s.closeSchemaDrop()
	s.beginSchemaDrop(83, "main", "replacement")
	next(ui.Services{})
	if calls.Load() != 0 || s.schemaDrop.target.Object.Name != "replacement" || s.schemaDrop.busy {
		t.Fatal("closed continuation acted on replacement")
	}
	// Starting a different table action revokes a deferred deletion, even when
	// that other action is unavailable in the current optional binding.
	s.closeSchemaDrop()
	s.beginSchemaDrop(83, "main", "t")
	tt.Frame()
	if err := tt.Click("重新读取结构"); err != nil {
		t.Fatal(err)
	}
	waitSchema(t, tt, func() bool { return !s.TablePending() })
	confirmDrop(t, tt)
	s.BeginTableChange(83, "main", "t", mysqlsvc.MySQLTableCreate)
	next(ui.Services{})
	if s.schemaDrop != nil || calls.Load() != 0 {
		t.Fatal("new table action retained a deferred deletion")
	}
	s.beginSchemaDrop(83, "main", "replacement")
	old := s.schemaDrop
	results := make(chan schemaDropReply, 1)
	old.busy, old.results = true, results
	s.closeSchemaDrop()
	s.beginSchemaDrop(83, "main", "another")
	results <- schemaDropReply{snapshot: contract.SchemaDesignSnapshot{Target: old.target}}
	s.Consume(ui.Services{})
	if s.schemaDrop.original != nil || s.schemaDrop.err != "" {
		t.Fatal("closed read receipt applied to new scope")
	}
}

func TestSchemaDropCancellationWaitsForAcknowledgementAndBlocksReplay(t *testing.T) {
	entered, ack := make(chan struct{}), make(chan struct{})
	s, _, tt := dropTestState(t, func(ctx context.Context, _ contract.SchemaDesignSnapshot) contract.SchemaDesignOutcome {
		close(entered)
		<-ctx.Done()
		<-ack
		return contract.SchemaDesignOutcome{State: "rolled_back", Error: ctx.Err()}
	})
	confirmDrop(t, tt)
	<-entered
	if err := tt.Click("取消"); err != nil {
		t.Fatal(err)
	}
	if !s.TablePending() || !s.schemaDrop.cancelRequested || s.CanDropTable(83, "main", "t") {
		t.Fatal("cancel released ownership before ack")
	}
	s.BeginTableChange(83, "main", "other", mysqlsvc.MySQLTableDrop)
	if s.schemaDrop.target.Object.Name != "t" {
		t.Fatal("pending drop was replaced")
	}
	close(ack)
	waitSchema(t, tt, func() bool { return !s.TablePending() })
	if !s.schemaDrop.blocked || s.schemaDrop.cancelRequested || s.TableError() == "" {
		t.Fatal("cancel allowed silent replay")
	}
}

func TestSchemaDropParkedMutationReceiptRetainsScopeAndTerminalCloseRevokesIt(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		t.Run(map[bool]string{false: "parked", true: "closed"}[terminal], func(t *testing.T) {
			s, w, _ := dropTestState(t, dropCommit)
			var got *SchemaDropEvent
			s.deps.SchemaDropped = func(_ ui.Services, event SchemaDropEvent) { got = &event }
			x := s.schemaDrop
			out := dropCommit(context.Background(), x.original.Clone())
			results := make(chan schemaDropReply, 1)
			x.results, x.busy, x.applying = results, true, true
			w.ProfileID = 99
			if terminal {
				s.CloseAll()
			}
			results <- schemaDropReply{outcome: &out}
			s.Consume(ui.Services{})
			if terminal {
				if got != nil {
					t.Fatal("closed host received mutation")
				}
			} else if got == nil || got.Original != x.target || got.Generation != 9 || s.schemaDrop != nil {
				t.Fatal("parked receipt lost original scope")
			}
		})
	}
}

func TestSchemaDropFailureKeepsConfirmationAndRequiresExplicitRead(t *testing.T) {
	s, _, tt := dropTestState(t, func(context.Context, contract.SchemaDesignSnapshot) contract.SchemaDesignOutcome {
		return contract.SchemaDesignOutcome{State: "rolled_back", Error: errors.New("dependency blocks drop")}
	})
	confirmDrop(t, tt)
	waitSchema(t, tt, func() bool { return !s.TablePending() })
	if s.schemaDrop.confirmation != "t" || !s.schemaDrop.blocked || !tt.HasText("dependency blocks drop") {
		t.Fatal("failed drop lost original state")
	}
	if err := tt.Click("重新读取结构"); err != nil {
		t.Fatal(err)
	}
	waitSchema(t, tt, func() bool { return !s.TablePending() })
	if s.schemaDrop.blocked || s.schemaDrop.confirmation != "" {
		t.Fatal("explicit reread kept old confirmation")
	}
}

func TestSchemaDropHostReceiptCannotCloseNewModal(t *testing.T) {
	s, _, tt := dropTestState(t, dropCommit)
	s.deps.SchemaDropped = func(_ ui.Services, _ SchemaDropEvent) { s.beginSchemaDrop(83, "main", "replacement") }
	confirmDrop(t, tt)
	waitSchema(t, tt, func() bool { return !s.TablePending() })
	if s.schemaDrop == nil || s.schemaDrop.target.Object.Name != "replacement" || !s.schemaDrop.open {
		t.Fatal("old host receipt closed the new modal")
	}
}

func TestSchemaDropMenuUsesOptionalBindingAndRejectsOldOpeningGeneration(t *testing.T) {
	s, w, _ := dropTestState(t, dropCommit)
	s.deps.Browser = func() BrowserSnapshot {
		return BrowserSnapshot{ProfileID: 83, Database: "main", Connected: true, Opened: true}
	}
	s.deps.SessionFor = func(int64) Session { return *w }
	s.closeSchemaDrop()
	tt := ui.NewTester(func(c *ui.Context) {
		s.Consume(c.Services())
		ui.Button(c, "Table menu").Menu(func(m *ui.Menu) { s.ContextMenu(c.Services(), m, 83, "main", "t", "表") })
		s.Modals(c)
	}, 900, 700)
	tt.Frame()
	if err := tt.Click("Table menu"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("截断表"); err == nil {
		t.Fatal("SQLite acquired unsupported MySQL truncate")
	}
	if err := tt.ChooseMenuItem("复制表", "仅结构"); err == nil {
		t.Fatal("SQLite acquired an unimplemented copy operation")
	}
	if err := tt.ChooseMenuItem("删除表"); err != nil {
		t.Fatal("optional drop binding did not enable menu", err)
	}
	if !s.TableDialogOpen() {
		t.Fatal("drop menu did not open the native preview")
	}
	s.closeSchemaDrop()
	tt.Frame()
	if err := tt.Click("Table menu"); err != nil {
		t.Fatal(err)
	}
	w.Generation++
	if err := tt.ChooseMenuItem("删除表"); err != nil {
		t.Fatal(err)
	}
	if s.TableDialogOpen() {
		t.Fatal("old opening generation targeted the new session")
	}
	// An incomplete/read-only binding keeps deletion visibly disabled.
	s.deps.SchemaDesignFor = func(int64) contract.SchemaDesignOperations {
		return contract.SchemaDesignOperations{CanDrop: func(contract.SchemaDesignTarget) bool { return false }, Drop: dropCommit}
	}
	tt.Frame()
	if err := tt.Click("Table menu"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("删除表"); err == nil {
		t.Fatal("incomplete binding enabled destructive menu")
	}
}
