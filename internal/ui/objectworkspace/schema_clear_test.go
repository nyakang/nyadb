package objectworkspace

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

func clearUIOutcome(id string, request contract.SchemaClearRequest) contract.SchemaDesignOutcome {
	actual := request.Original.Clone()
	out := contract.SchemaDesignOutcome{State: "committed", Verified: true, Rows: 2, Actual: &actual}
	out.Receipt = &contract.SchemaDesignWriteReceipt{DocumentID: id, Sequence: 1, Operation: "clear", Original: request.Original.Clone(), Destination: request.Original.Target.Object.Name, ResetIdentity: request.ResetIdentity, State: out.State, Verified: true, Rows: out.Rows, Actual: &actual, Statements: []string{"DELETE FROM main.t"}, NativeJSON: `{"results":[{"rows_affected":2}],"raft_index":9007199254740993}`}
	return out
}

func clearTestState(t *testing.T, run func(context.Context, string, contract.SchemaClearRequest) contract.SchemaDesignOutcome) (*State, *Session, *ui.Tester, *schemaOwnerTestLease) {
	t.Helper()
	s, w, _ := dropTestState(t, dropCommit)
	s.closeSchemaDrop()
	ops := s.deps.SchemaDesignFor(83)
	load := ops.Snapshot
	ops.Snapshot = func(ctx context.Context, target contract.SchemaDesignTarget) (contract.SchemaDesignSnapshot, error) {
		a, err := load(ctx, target)
		a.Schema.DDL = "CREATE TABLE t(id INTEGER PRIMARY KEY AUTOINCREMENT,v BLOB)"
		a.Schema.Table, _ = schemaddl.ParseSQLite(a.Schema.DDL)
		a.Schema.SQLite = &schemaddl.SQLiteCatalog{Version: 12, Entries: []schemaddl.SQLiteCatalogEntry{{Type: "table", Name: "t", Table: "t", Root: 2, SQL: a.Schema.DDL}}}
		return a, err
	}
	ops.CanClear = func(target contract.SchemaDesignTarget) bool { return target.Object.ProfileID == 83 }
	ops.Clear = func(ctx context.Context, r contract.SchemaClearRequest) contract.SchemaDesignOutcome {
		return run(ctx, "unused-session-binding", r)
	}
	l := &schemaOwnerTestLease{ops: ops}
	s.deps.SchemaDesignFor = func(int64) contract.SchemaDesignOperations { return ops }
	s.deps.SchemaDocumentsFor = func(int64) (contract.SchemaDesignDocumentsProvider, error) {
		return schemaOwnerTestProvider(func(r contract.SchemaDesignDocumentRequest) (contract.SchemaDesignDocumentLease, error) {
			l.ops.Clear = func(ctx context.Context, request contract.SchemaClearRequest) contract.SchemaDesignOutcome {
				return run(ctx, r.DocumentID, request)
			}
			return l, nil
		}), nil
	}
	s.BeginTableChange(83, "main", "t", mysqlsvc.MySQLTableDeleteRows)
	tt := tableModalTester(s)
	tt.Frame()
	if err := tt.Click("重新读取结构"); err != nil {
		t.Fatal(err)
	}
	waitSchema(t, tt, func() bool { return !s.TablePending() })
	if s.schemaClear == nil || s.schemaClear.original == nil || s.schemaClear.owner == nil {
		t.Fatal("owned clear document missing", s.TableError())
	}
	return s, w, tt, l
}

func confirmClear(t *testing.T, tt *ui.Tester) {
	t.Helper()
	if err := tt.Click("确认原表名称"); err != nil {
		t.Fatal(err)
	}
	tt.Type("t")
	if err := tt.Click("确认清空表"); err != nil {
		t.Fatal(err)
	}
}

func TestSchemaClearGuardCapturesPolicyAndCannotApproveChangedRequest(t *testing.T) {
	for _, change := range []string{"reset-policy", "schema", "cancel-decision", "unchanged"} {
		t.Run(change, func(t *testing.T) {
			var calls atomic.Int32
			s, _, tt, _ := clearTestState(t, func(_ context.Context, id string, r contract.SchemaClearRequest) contract.SchemaDesignOutcome {
				calls.Add(1)
				return clearUIOutcome(id, r)
			})
			var next func(ui.Services)
			pending := true
			s.deps.BeforeClear = func(_ ui.Services, id int64, db, table string, f func(ui.Services)) bool {
				if id != 83 || db != "main" || table != "t" {
					t.Fatal("clear guard changed original scope")
				}
				next = f
				return true
			}
			s.deps.ClearGuardPending = func(id int64, db string) bool { return pending }
			if !tt.HasText("清空 SQL 预览") || !tt.HasText("删除触发器和外键级联会执行，可能修改同库其他表；任一步失败则回滚。") || s.SchemaTableOriginalOutcome("clear") != nil {
				t.Fatal("clear preview omitted effects or fabricated a receipt")
			}
			confirmClear(t, tt)
			if next == nil || calls.Load() != 0 {
				t.Fatal("clear bypassed database draft guard")
			}
			switch change {
			case "reset-policy":
				s.schemaClear.resetIdentity = true
			case "schema":
				s.schemaClear.original.Schema.Table.Clauses[0].SQL = "INTEGER"
			case "cancel-decision":
				pending = false
				tt.Frame()
			}
			next(ui.Services{})
			if change == "unchanged" {
				waitSchema(t, tt, func() bool { return !s.TablePending() })
				if calls.Load() != 1 || !s.schemaClear.completed || s.SchemaTableOriginalOutcome("clear").Receipt.ResetIdentity {
					t.Fatal("clear lost original policy or successful receipt")
				}
			} else if calls.Load() != 0 || s.TablePending() || s.SchemaTableOriginalOutcome("clear") != nil || s.schemaClear.guarding {
				t.Fatal("stale guard executed or left a blocked modal hidden")
			}
		})
	}
}

func TestSchemaClearCancellationWaitsOriginalReceiptAndKeepsCapturedResetChoice(t *testing.T) {
	entered, ack := make(chan contract.SchemaClearRequest, 1), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(ack) })
	s, _, tt, _ := clearTestState(t, func(_ context.Context, id string, r contract.SchemaClearRequest) contract.SchemaDesignOutcome {
		entered <- r
		<-ack
		return clearUIOutcome(id, r)
	})
	s.schemaClear.resetIdentity = true
	confirmClear(t, tt)
	select {
	case r := <-entered:
		if !r.ResetIdentity {
			t.Fatal("reset choice lost")
		}
	case <-time.After(time.Second):
		t.Fatal("clear executor not entered")
	}
	s.schemaClear.resetIdentity = false
	s.schemaClear.original.Schema.Table.Clauses[0].SQL = "UI mutation"
	if err := tt.Click("取消"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !s.TablePending() || !s.TableDialogOpen() || !s.schemaClear.cancelRequested || s.SchemaTableOriginalOutcome("clear") != nil {
		t.Fatal("cancel abandoned original acknowledgement")
	}
	once.Do(func() { close(ack) })
	waitSchema(t, tt, func() bool { return !s.TablePending() })
	out := s.SchemaTableOriginalOutcome("clear")
	if out == nil || !out.Verified || !out.Receipt.ResetIdentity || out.Receipt.Original.Schema.Table.Clauses[0].SQL == "UI mutation" {
		t.Fatal("clear consumed mutable policy or lost receipt", out)
	}
}

func TestSchemaClearOwnedParkedLateReceiptDisconnectAndTerminalClose(t *testing.T) {
	for _, mode := range []string{"parked", "disconnect", "close"} {
		t.Run(mode, func(t *testing.T) {
			s, w, _, l := clearTestState(t, func(_ context.Context, id string, r contract.SchemaClearRequest) contract.SchemaDesignOutcome {
				return clearUIOutcome(id, r)
			})
			x := s.schemaClear
			request := contract.SchemaClearRequest{Original: x.original.Clone(), ResetIdentity: true}
			out := clearUIOutcome(x.owner.id, request)
			ch := make(chan schemaClearReply, 1)
			x.applied = &request
			x.busy, x.applying = true, true
			x.results = ch
			var event *SchemaClearEvent
			s.deps.SchemaCleared = func(_ ui.Services, e SchemaClearEvent) { event = &e }
			if mode == "parked" {
				w.ProfileID = 999
			} else if mode == "disconnect" {
				w.Connected = false
				w.Generation++
				s.CancelTableFor(83)
			} else {
				s.CloseAll()
			}
			ch <- schemaClearReply{outcome: &out}
			s.Consume(ui.Services{})
			if mode == "close" {
				if event != nil || s.SchemaTableOriginalOutcome("clear") != nil || l.closes.Load() != 1 {
					t.Fatal("terminal clear close published callback or retained receipt")
				}
				return
			}
			if mode == "parked" {
				if event != nil || !s.TablePending() {
					t.Fatal("another profile consumed original clear")
				}
				w.ProfileID = 83
				s.Consume(ui.Services{})
			}
			retained := s.SchemaTableOriginalOutcome("clear")
			if event == nil || event.Original != request.Original.Target || event.Generation != 9 || retained == nil || retained.Receipt.NativeJSON != out.Receipt.NativeJSON {
				t.Fatal("clear late receipt lost original scope")
			}
			w.Connected = false
			s.CancelTableFor(83)
			s.Consume(ui.Services{})
			if !reflect.DeepEqual(retained, s.SchemaTableOriginalOutcome("clear")) || !s.TableDialogOpen() {
				t.Fatal("disconnect erased owned settled clear receipt")
			}
			s.closeSchemaClear()
			if s.SchemaTableOriginalOutcome("clear") != nil || l.closes.Load() != 1 {
				t.Fatal("explicit clear close retained caches")
			}
		})
	}
}

func TestSchemaClearOwnedMismatchedResetReceiptRemainsOriginalAndBlocksReplay(t *testing.T) {
	var calls atomic.Int32
	s, _, tt, _ := clearTestState(t, func(_ context.Context, id string, r contract.SchemaClearRequest) contract.SchemaDesignOutcome {
		calls.Add(1)
		out := clearUIOutcome(id, r)
		out.Receipt.ResetIdentity = !r.ResetIdentity
		return out
	})
	confirmClear(t, tt)
	waitSchema(t, tt, func() bool { return !s.TablePending() })
	out := s.SchemaTableOriginalOutcome("clear")
	if out == nil || out.Verified || !out.Receipt.Verified || !out.Receipt.ResetIdentity || !errors.Is(out.Error, contract.ErrSchemaCommitUncertain) || !s.schemaClear.owner.blocked {
		t.Fatal("mismatched clear policy accepted or original evidence overwritten", out)
	}
	s.readSchemaClear(ui.Services{})
	waitSchema(t, tt, func() bool { return !s.TablePending() })
	s.schemaClear.confirmation = "t"
	s.applySchemaClear(ui.Services{})
	if calls.Load() != 1 || !reflect.DeepEqual(out, s.SchemaTableOriginalOutcome("clear")) {
		t.Fatal("fresh read replaced original unknown policy or replayed clear")
	}
}
