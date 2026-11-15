package objectworkspace

import (
	"context"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

func TestSchemaOwnedTableDialogsParkOriginalReceiptsAndRetainSuccessOrUnknownUntilClose(t *testing.T) {
	for _, operation := range []string{"drop", "copy", "rename"} {
		for _, state := range []string{"uncertain", "committed"} {
			for _, scope := range []string{"stable", "host-disconnect", "disconnect-before-ack", "generation-after"} {
				t.Run(operation+"/"+state+"/"+scope, func(t *testing.T) {
					s, w, _ := dropTestState(t, dropCommit)
					s.closeSchemaDrop()
					target := s.deps.SchemaTarget(83, "main", "t")
					target.Object.Schema = ""
					s.deps.SchemaTarget = func(id int64, _, name string) contract.SchemaDesignTarget {
						next := target
						next.Object.ProfileID, next.Object.Name = id, name
						return next
					}
					ddl := "CREATE TABLE t(id INTEGER PRIMARY KEY,value TEXT)"
					table, err := schemaddl.ParseSQLite(ddl)
					if err != nil {
						t.Fatal(err)
					}
					original := contract.SchemaDesignSnapshot{Target: target, Schema: schemaddl.Snapshot{Engine: "sqlite", Revision: "original", Exists: true, DDL: ddl, Table: table, SQLite: &schemaddl.SQLiteCatalog{Version: 12, Entries: []schemaddl.SQLiteCatalogEntry{{Type: "table", Name: "t", Table: "t", Root: 2, SQL: ddl}}}}}
					var reads, writes atomic.Int32
					started := make(chan contract.SchemaDesignWriteReceipt, 1)
					ack := make(chan struct{})
					l := &schemaOwnerTestLease{}
					l.ops.Snapshot = func(_ context.Context, target contract.SchemaDesignTarget) (contract.SchemaDesignSnapshot, error) {
						reads.Add(1)
						if target != original.Target {
							t.Error("read retargeted original table")
						}
						return original.Clone(), nil
					}
					can := func(next contract.SchemaDesignTarget) bool { return next == target }
					l.ops.CanDrop, l.ops.CanCopy, l.ops.CanRename = can, can, can
					run := func(receipt contract.SchemaDesignWriteReceipt) contract.SchemaDesignOutcome {
						writes.Add(1)
						receipt.DocumentID = "schema-" + operation + ":2"
						receipt.Sequence = 1
						receipt.State, receipt.Verified = state, state == "committed"
						receipt.Statements = []string{"original captured " + operation + " statement"}
						receipt.NativeJSON = `{"results":[{"rows_affected":2}],"raft_index":9007199254740993}`
						receipt.Rows = 2
						if receipt.Verified {
							actual := receipt.Original.Clone()
							actual.Target.Object.Name, actual.Schema.Table.Name = receipt.Destination, receipt.Destination
							actual.Schema.Exists = operation != "drop"
							actual.Schema.SQLite.Version++
							if operation == "drop" {
								actual.Schema.SQLite.Entries = nil
							} else {
								actual.Schema.SQLite.Entries[0].Name, actual.Schema.SQLite.Entries[0].Table = receipt.Destination, receipt.Destination
							}
							receipt.Actual = &actual
						}
						started <- receipt.Clone()
						<-ack
						return contract.SchemaDesignOutcome{State: state, Verified: receipt.Verified, Rows: receipt.Rows, Actual: receipt.Actual, Receipt: &receipt}
					}
					l.ops.Drop = func(_ context.Context, r contract.SchemaDesignSnapshot) contract.SchemaDesignOutcome {
						return run(contract.SchemaDesignWriteReceipt{Operation: "drop", Original: r.Clone(), Destination: r.Target.Object.Name})
					}
					l.ops.Copy = func(_ context.Context, r contract.SchemaCopyRequest) contract.SchemaDesignOutcome {
						return run(contract.SchemaDesignWriteReceipt{Operation: "copy", Original: r.Original.Clone(), Destination: r.Destination, WithData: r.WithData, RebindSelfReferences: r.RebindSelfReferences, PreserveRowID: r.PreserveRowID})
					}
					l.ops.Rename = func(_ context.Context, r contract.SchemaRenameRequest) contract.SchemaDesignOutcome {
						return run(contract.SchemaDesignWriteReceipt{Operation: "rename", Original: r.Original.Clone(), Destination: r.Destination})
					}
					s.deps.SchemaDesignFor = func(int64) contract.SchemaDesignOperations { return l.ops }
					reserves := 0
					s.deps.SchemaDocumentsFor = func(int64) (contract.SchemaDesignDocumentsProvider, error) {
						return schemaOwnerTestProvider(func(r contract.SchemaDesignDocumentRequest) (contract.SchemaDesignDocumentLease, error) {
							reserves++
							if r.Target != target || r.DocumentID != "schema-"+operation+":2" {
								t.Error("modal reservation changed identity", r)
							}
							return l, nil
						}), nil
					}
					var event *contract.SchemaDesignOutcome
					capture := func(out contract.SchemaDesignOutcome) {
						if scope == "host-disconnect" {
							w.Connected = false
							s.CancelTableFor(83)
						}
						snapshot := out.Clone()
						event = &snapshot
						out.Receipt.NativeJSON = "host mutation"
						if out.Actual != nil {
							out.Actual.Schema.Table.Name = "host mutation"
						}
					}
					s.deps.SchemaDropped = func(_ ui.Services, e SchemaDropEvent) { capture(e.Outcome) }
					s.deps.SchemaCopied = func(_ ui.Services, e SchemaCopyEvent) {
						if e.Destination != "t_copy" {
							t.Error("copy destination was retargeted")
						}
						capture(e.Outcome)
					}
					s.deps.SchemaRenamed = func(_ ui.Services, e SchemaRenameEvent) {
						if e.Destination != "renamed" {
							t.Error("rename destination was retargeted")
						}
						capture(e.Outcome)
					}
					var read, apply, closeModal func(ui.Services)
					var owner func() *schemaDocumentOwner
					var results func() int
					switch operation {
					case "drop":
						s.beginSchemaDrop(83, "main", "t")
						read, apply = s.readSchemaDrop, s.applySchemaDrop
						owner = func() *schemaDocumentOwner { return s.schemaDrop.owner }
						results = func() int { return len(s.schemaDrop.results) }
						closeModal = func(ui.Services) { s.closeSchemaDrop() }
					case "copy":
						s.beginSchemaCopy(83, "main", "t", true)
						read, apply = s.readSchemaCopy, s.applySchemaCopy
						owner = func() *schemaDocumentOwner { return s.schemaCopy.owner }
						results = func() int { return len(s.schemaCopy.results) }
						closeModal = func(ui.Services) { s.closeSchemaCopy() }
					case "rename":
						s.beginSchemaRenameOperation(83, "main", "t")
						read, apply = s.readSchemaRename, s.applySchemaRename
						owner = func() *schemaDocumentOwner { return s.schemaRename.owner }
						results = func() int { return len(s.schemaRename.results) }
						closeModal = func(ui.Services) { s.closeSchemaRename() }
					}
					tt := tableModalTester(s)
					tt.Frame()
					read(ui.Services{})
					waitSchema(t, tt, func() bool { return !s.TablePending() })
					if operation == "drop" {
						s.schemaDrop.confirmation = "t"
					}
					if operation == "rename" {
						s.schemaRename.destination = "renamed"
					}
					apply(ui.Services{})
					captured := <-started
					if operation == "copy" {
						s.schemaCopy.destination, s.schemaCopy.withData, s.schemaCopy.rebindSelf, s.schemaCopy.preserveRowID = "later", false, false, false
						if !captured.WithData || !captured.RebindSelfReferences || !captured.PreserveRowID {
							t.Fatal("original copy choices omitted")
						}
					}
					if operation == "rename" {
						s.schemaRename.destination = "later"
					}
					w.ProfileID = 84
					close(ack)
					waitSchema(t, tt, func() bool { return results() == 1 })
					if !s.TablePending() || event != nil || owner().original() != nil {
						t.Fatal("parked original modal result consumed on another profile")
					}
					w.ProfileID = 83
					if scope == "disconnect-before-ack" {
						w.Connected = false
						s.CancelTableFor(83)
					}
					waitSchema(t, tt, func() bool { return !s.TablePending() })
					retained := s.SchemaTableOriginalOutcome(operation)
					if retained == nil || event == nil || !reflect.DeepEqual(*retained.Receipt, captured) || retained.Verified != (state == "committed") || reserves != 1 || writes.Load() != 1 {
						t.Fatal("original modal receipt lost, mutated or incorrectly normalized", retained, event)
					}
					if scope == "generation-after" {
						w.Generation++
						s.CancelTableFor(83)
					}
					for i := 0; i < 4; i++ {
						tt.Frame()
					}
					if !s.TableDialogOpen() || !reflect.DeepEqual(retained, s.SchemaTableOriginalOutcome(operation)) {
						t.Fatal("original receipt disappeared after original scope invalidation")
					}
					if state == "uncertain" {
						if !owner().blocked {
							t.Fatal("unknown modal write did not block replay")
						}
						read(ui.Services{})
						waitSchema(t, tt, func() bool { return !s.TablePending() })
						apply(ui.Services{})
						expectedReads := int32(1)
						if scope == "stable" {
							expectedReads++
						}
						if reads.Load() != expectedReads || writes.Load() != 1 || !reflect.DeepEqual(retained, s.SchemaTableOriginalOutcome(operation)) {
							t.Fatal("fresh modal read erased evidence or replayed original write")
						}
					} else {
						if !owner().showReceipt {
							t.Fatal("successful modal closed before original receipt could be viewed")
						}
						read(ui.Services{})
						apply(ui.Services{})
						if reads.Load() != 1 || writes.Load() != 1 {
							t.Fatal("completed modal admitted another read or write")
						}
					}
					x := owner()
					closeModal(ui.Services{})
					if l.closes.Load() != 1 || x.last != nil || x.lease != nil || s.SchemaTableOriginalOutcome(operation) != nil {
						t.Fatal("modal terminal close retained original lease or evidence")
					}
				})
			}
		}
	}
}
