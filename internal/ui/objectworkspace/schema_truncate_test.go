package objectworkspace

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

func TestSchemaTruncateGuardCannotApproveChangedOperationOrSnapshot(t *testing.T) {
	for _, change := range []string{"operation", "snapshot", "unchanged"} {
		t.Run(change, func(t *testing.T) {
			var calls atomic.Int32
			w := Session{ProfileID: 83, Generation: 9, Connected: true}
			target := contract.SchemaDesignTarget{Object: contract.ObjectRef{ProfileID: 83, Schema: "main", Kind: "table", Name: "t"}, Generation: 73}
			before := contract.SchemaDesignSnapshot{Target: target, Schema: schemaddl.Snapshot{Engine: "databend", Exists: true, Revision: "original", DDL: "CREATE TABLE t (id BIGINT UNSIGNED NOT NULL) ENGINE=FUSE", Table: schemaddl.Table{Name: "t"}, Databend: &schemaddl.DatabendTableIdentity{Database: "main", TableID: "62"}}}
			ops := contract.SchemaDesignOperations{CanTruncate: func(t contract.SchemaDesignTarget) bool { return t == target }, Snapshot: func(context.Context, contract.SchemaDesignTarget) (contract.SchemaDesignSnapshot, error) {
				return before.Clone(), nil
			}}
			var proceed func(ui.Services)
			s := New(Dependencies{Session: func() Session { return w }, SessionFor: func(int64) Session { return w },
				SchemaTarget:    func(int64, string, string) contract.SchemaDesignTarget { return target },
				Engine:          func(int64) string { return "databend" },
				SchemaDesignFor: func(int64) contract.SchemaDesignOperations { return ops },
				SchemaDocumentsFor: func(int64) (contract.SchemaDesignDocumentsProvider, error) {
					return schemaOwnerTestProvider(func(r contract.SchemaDesignDocumentRequest) (contract.SchemaDesignDocumentLease, error) {
						bound := ops
						bound.Truncate = func(_ context.Context, original contract.SchemaDesignSnapshot) contract.SchemaDesignOutcome {
							calls.Add(1)
							out := clearUIOutcome(r.DocumentID, contract.SchemaClearRequest{Original: original})
							out.Rows, out.Receipt.Rows = 0, 0
							out.Receipt.Operation = "truncate"
							out.Receipt.Statements = []string{"TRUNCATE TABLE \"main\".\"t\""}
							return out
						}
						return &schemaOwnerTestLease{ops: bound}, nil
					}), nil
				},
				BeforeClear: func(_ ui.Services, _ int64, _, _ string, next func(ui.Services)) bool { proceed = next; return true },
			})
			// The session-level executor is only a capability stub; writes must
			// use the captured document provider above.
			ops.Truncate = func(context.Context, contract.SchemaDesignSnapshot) contract.SchemaDesignOutcome {
				t.Error("session-level truncate executed")
				return contract.SchemaDesignOutcome{}
			}
			t.Cleanup(s.CloseAll)
			s.BeginTableChange(83, "main", "t", mysqlsvc.MySQLTableTruncate)
			tt := tableModalTester(s)
			tt.Frame()
			if err := tt.Click("重新读取结构"); err != nil {
				t.Fatal(err)
			}
			waitSchema(t, tt, func() bool { return !s.TablePending() })
			if s.schemaClear == nil || s.schemaClear.original == nil {
				t.Fatal("truncate preview missing", s.TableError())
			}
			s.schemaClear.confirmation = "t"
			s.applySchemaClear(ui.Services{})
			if proceed == nil || calls.Load() != 0 {
				t.Fatal("truncate bypassed leave guard")
			}
			switch change {
			case "operation":
				s.schemaClear.truncate = false
			case "snapshot":
				s.schemaClear.original.Schema.Databend.TableID = "63"
			}
			proceed(ui.Services{})
			waitSchema(t, tt, func() bool { return !s.TablePending() })
			if change == "unchanged" {
				out := s.SchemaTableOriginalOutcome("truncate")
				if calls.Load() != 1 || out == nil || !out.Verified || out.Receipt.Operation != "truncate" || s.SchemaTableOriginalOutcome("clear") != nil {
					t.Fatal("original truncate receipt unavailable")
				}
			} else if calls.Load() != 0 || !s.schemaClear.blocked {
				t.Fatal("changed guarded truncate was dispatched")
			}
		})
	}
}
