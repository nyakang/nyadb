package objectworkspace

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

func renameCommit(_ context.Context, r contract.SchemaRenameRequest) contract.SchemaDesignOutcome {
	a := r.Original.Clone()
	a.Target.Object.Name, a.Schema.Table.Name = r.Destination, r.Destination
	return contract.SchemaDesignOutcome{State: "committed", Verified: true, Actual: &a}
}

func renameTestState(t *testing.T, run func(context.Context, contract.SchemaRenameRequest) contract.SchemaDesignOutcome) (*State, *Session, *ui.Tester) {
	t.Helper()
	s, w, _ := dropTestState(t, dropCommit)
	s.closeSchemaDrop()
	s.deps.SchemaDesignFor = func(int64) contract.SchemaDesignOperations {
		return contract.SchemaDesignOperations{
			CanRename: func(target contract.SchemaDesignTarget) bool { return target.Object.ProfileID == 83 }, Rename: run,
			Snapshot: func(_ context.Context, target contract.SchemaDesignTarget) (contract.SchemaDesignSnapshot, error) {
				p := &schemaddl.OpenGaussCatalog{Database: "main", Namespace: "main", Name: target.Object.Name, DatabaseOID: 1, NamespaceOID: 2, RelationOID: 3, RoleOID: 4, CanManage: true,
					Entries: []schemaddl.OpenGaussCatalogEntry{{Kind: "namespace", OID: 2, Data: json.RawMessage(`{"nspname":"main"}`)},
						{Kind: "relation", OID: 3, Data: json.RawMessage(`{"relname":"` + target.Object.Name + `","relkind":"r","relpersistence":"p","relfilenode":10}`)}}}
				revision, err := p.Fingerprint()
				return contract.SchemaDesignSnapshot{Target: target, Schema: schemaddl.Snapshot{Engine: "opengauss", Exists: true, Revision: revision, OpenGauss: p, Table: schemaddl.Table{Name: target.Object.Name}}}, err
			},
		}
	}
	s.BeginTableChange(83, "main", "t", mysqlsvc.MySQLTableRename)
	tt := tableModalTester(s)
	tt.Frame()
	if err := tt.Click("重新读取结构"); err != nil {
		t.Fatal(err)
	}
	waitSchema(t, tt, func() bool { return !s.TablePending() })
	s.schemaRename.destination = "renamed"
	tt.Frame()
	return s, w, tt
}

func TestSchemaRenamePreviewGuardAndCapturedDestination(t *testing.T) {
	entered, ack := make(chan contract.SchemaRenameRequest, 1), make(chan struct{})
	s, _, tt := renameTestState(t, func(ctx context.Context, r contract.SchemaRenameRequest) contract.SchemaDesignOutcome {
		entered <- r
		<-ack
		return renameCommit(ctx, r)
	})
	if !tt.HasText("重命名 SQL 预览") || s.tableDialog {
		t.Fatal("independent native preview missing")
	}
	var next func(ui.Services)
	s.deps.BeforeRename = func(_ ui.Services, id int64, db, table string, f func(ui.Services)) bool {
		if id != 83 || db != "main" || table != "t" {
			t.Fatal("guard retargeted")
		}
		next = f
		return true
	}
	if err := tt.Click("确认重命名表"); err != nil {
		t.Fatal(err)
	}
	if next == nil || s.TablePending() {
		t.Fatal("draft decision bypassed")
	}
	next(ui.Services{})
	r := <-entered
	if r.Destination != "renamed" || r.Original.Target != s.schemaRename.target {
		t.Fatal("original scope lost")
	}
	s.schemaRename.destination = "presentation changed"
	s.schemaRename.original.Schema.OpenGauss.Entries[0].Data[0] = '['
	var got *SchemaRenameEvent
	s.deps.SchemaRenamed = func(_ ui.Services, event SchemaRenameEvent) { got = &event }
	close(ack)
	waitSchema(t, tt, func() bool { return !s.TablePending() })
	if got == nil || got.Destination != "renamed" || got.Original != r.Original.Target || s.schemaRename != nil {
		t.Fatal("receipt used mutable UI request")
	}
}

func TestSchemaRenameCancelledGuardRestoresPreviewAndRevokesContinuation(t *testing.T) {
	var calls atomic.Int32
	s, _, tt := renameTestState(t, func(ctx context.Context, r contract.SchemaRenameRequest) contract.SchemaDesignOutcome {
		calls.Add(1)
		return renameCommit(ctx, r)
	})
	pending := true
	var next func(ui.Services)
	s.deps.BeforeRename = func(_ ui.Services, _ int64, _, _ string, f func(ui.Services)) bool { next = f; return true }
	s.deps.TableGuardPending = func(int64, string, string) bool { return pending }
	if err := tt.Click("确认重命名表"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if s.TableDialogOpen() {
		t.Fatal("preview covered document decision")
	}
	pending = false
	tt.Frame()
	if !s.TableDialogOpen() {
		t.Fatal("cancelled decision left preview hidden")
	}
	next(ui.Services{})
	if calls.Load() != 0 || s.TablePending() {
		t.Fatal("cancelled decision launched late rename")
	}
	if err := tt.Click("确认重命名表"); err != nil {
		t.Fatal(err)
	}
	s.closeSchemaRename()
	s.beginSchemaRenameOperation(83, "main", "replacement")
	next(ui.Services{})
	if calls.Load() != 0 || s.schemaRename.target.Object.Name != "replacement" {
		t.Fatal("old guard acted on replacement")
	}
}

func TestSchemaRenameCancellationWaitsForAcknowledgementAndExplicitRead(t *testing.T) {
	entered, ack := make(chan struct{}), make(chan struct{})
	s, _, tt := renameTestState(t, func(ctx context.Context, _ contract.SchemaRenameRequest) contract.SchemaDesignOutcome {
		close(entered)
		<-ctx.Done()
		<-ack
		return contract.SchemaDesignOutcome{State: "rolled_back", Error: ctx.Err()}
	})
	if err := tt.Click("确认重命名表"); err != nil {
		t.Fatal(err)
	}
	<-entered
	if err := tt.Click("取消"); err != nil {
		t.Fatal(err)
	}
	if !s.TablePending() || !s.schemaRename.cancelRequested || s.CanRenameTable(83, "main", "t") || s.CanDropTable(83, "main", "t") {
		t.Fatal("cancel released owning mutation early")
	}
	s.BeginTableChange(83, "main", "other", mysqlsvc.MySQLTableDrop)
	if s.schemaRename == nil || s.schemaDrop != nil {
		t.Fatal("operation replaced before acknowledgement")
	}
	close(ack)
	waitSchema(t, tt, func() bool { return !s.TablePending() })
	if !s.schemaRename.blocked || s.TableError() == "" {
		t.Fatal("cancel enabled automatic replay")
	}
	if err := tt.Click("重新读取结构"); err != nil {
		t.Fatal(err)
	}
	waitSchema(t, tt, func() bool { return !s.TablePending() })
	if s.schemaRename.blocked || s.schemaRename.destination != "renamed" {
		t.Fatal("explicit reread lost destination")
	}
}

func TestSchemaRenameParkedReceiptAndTerminalCloseKeepOriginalScope(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		t.Run(map[bool]string{false: "parked", true: "closed"}[terminal], func(t *testing.T) {
			s, w, _ := renameTestState(t, renameCommit)
			var got *SchemaRenameEvent
			s.deps.SchemaRenamed = func(_ ui.Services, event SchemaRenameEvent) { got = &event }
			x := s.schemaRename
			r, _, err := s.schemaRenameRequest(x)
			if err != nil {
				t.Fatal(err)
			}
			x.applied = &r
			out := renameCommit(context.Background(), r)
			ch := make(chan schemaRenameReply, 1)
			x.results, x.busy, x.applying = ch, true, true
			w.ProfileID = 99
			if terminal {
				s.CloseAll()
			}
			ch <- schemaRenameReply{outcome: &out}
			s.Consume(ui.Services{})
			if terminal {
				if got != nil {
					t.Fatal("closed host received rename")
				}
			} else if got == nil || got.Original != x.target || got.Generation != 9 || s.schemaRename != nil {
				t.Fatal("parked receipt lost original scope")
			}
		})
	}
}

func TestSchemaRenameRejectsMalformedReceiptAndLeavesHostReplacementOpen(t *testing.T) {
	s, _, tt := renameTestState(t, func(ctx context.Context, r contract.SchemaRenameRequest) contract.SchemaDesignOutcome {
		out := renameCommit(ctx, r)
		out.Actual.Schema.Table.Name = "wrong"
		return out
	})
	if err := tt.Click("确认重命名表"); err != nil {
		t.Fatal(err)
	}
	waitSchema(t, tt, func() bool { return !s.TablePending() })
	if s.schemaRename == nil || !s.schemaRename.blocked || s.TableError() == "" {
		t.Fatal("malformed destination receipt trusted")
	}
	s.closeSchemaRename()
	s, _, tt = renameTestState(t, renameCommit)
	s.deps.SchemaRenamed = func(_ ui.Services, _ SchemaRenameEvent) { s.beginSchemaRenameOperation(83, "main", "replacement") }
	if err := tt.Click("确认重命名表"); err != nil {
		t.Fatal(err)
	}
	waitSchema(t, tt, func() bool { return !s.TablePending() })
	if s.schemaRename == nil || s.schemaRename.target.Object.Name != "replacement" || !s.schemaRename.open {
		t.Fatal("old receipt closed replacement")
	}
}

func TestSchemaRenameMenuUsesOptionalBindingAndCapturedGeneration(t *testing.T) {
	s, w, _ := renameTestState(t, renameCommit)
	s.closeSchemaRename()
	s.deps.Browser = func() BrowserSnapshot {
		return BrowserSnapshot{ProfileID: 83, Database: "main", Connected: true, Opened: true}
	}
	s.deps.SessionFor = func(int64) Session { return *w }
	tt := ui.NewTester(func(c *ui.Context) {
		s.Consume(c.Services())
		ui.Button(c, "Table menu").Menu(func(m *ui.Menu) { s.ContextMenu(c.Services(), m, 83, "main", "t", "表") })
		s.Modals(c)
	}, 900, 700)
	tt.Frame()
	if err := tt.Click("Table menu"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("重命名"); err != nil {
		t.Fatal(err)
	}
	if s.schemaRename == nil || s.tableDialog {
		t.Fatal("menu dispatched unrelated table designer")
	}
	s.closeSchemaRename()
	tt.Frame()
	if err := tt.Click("Table menu"); err != nil {
		t.Fatal(err)
	}
	w.Generation++
	if err := tt.ChooseMenuItem("重命名"); err != nil {
		t.Fatal(err)
	}
	if s.schemaRename != nil {
		t.Fatal("old menu targeted replacement session")
	}
	s.deps.SchemaDesignFor = func(int64) contract.SchemaDesignOperations {
		return contract.SchemaDesignOperations{CanRename: func(contract.SchemaDesignTarget) bool { return false }, Rename: renameCommit}
	}
	tt.Frame()
	if err := tt.Click("Table menu"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("重命名"); err == nil {
		t.Fatal("incomplete rename binding enabled")
	}
}
