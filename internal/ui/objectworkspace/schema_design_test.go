package objectworkspace

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

func schemaTestState(t *testing.T) (*State, *Document, *Session, *ui.Tester, ui.Services) {
	t.Helper()
	w := &Session{ProfileID: 83, Generation: 9, Connected: true}
	ddl := "CREATE TABLE t(id INTEGER PRIMARY KEY, value TEXT DEFAULT 'saved' COLLATE NOCASE)"
	table, err := schemaddl.ParseSQLite(ddl)
	if err != nil {
		t.Fatal(err)
	}
	object := contract.ObjectRef{ProfileID: 83, Catalog: "main", Schema: "main", Kind: "table", Name: "t"}
	s := New(Dependencies{Session: func() Session { return *w }, SessionFor: func(int64) Session { return *w }, Capabilities: func(int64) contract.Capabilities { return contract.Capabilities{} },
		SchemaTarget: func(id int64, database, name string) contract.SchemaDesignTarget {
			return contract.SchemaDesignTarget{Object: contract.ObjectRef{ProfileID: id, Catalog: "main", Schema: database, Kind: "table", Name: name}, Generation: 73}
		},
		SchemaDesignFor: func(int64) contract.SchemaDesignOperations {
			return contract.SchemaDesignOperations{CanApply: func(t contract.SchemaDesignTarget) bool { return t.Generation == 73 && t.Object.ProfileID == 83 }, Snapshot: func(_ context.Context, target contract.SchemaDesignTarget) (contract.SchemaDesignSnapshot, error) {
				return contract.SchemaDesignSnapshot{Target: target, Schema: schemaddl.Snapshot{Engine: "sqlite", Revision: "fixture", Exists: true, DDL: ddl, Table: table.Clone()}}, nil
			}, Apply: func(_ context.Context, r contract.SchemaDesignRequest) contract.SchemaDesignOutcome {
				actual := r.Original.Clone()
				actual.Schema.Table = r.Desired.Clone()
				actual.Target.Object.Name = r.Desired.Name
				return contract.SchemaDesignOutcome{State: "committed", Verified: true, Actual: &actual}
			}}
		},
	})
	d := s.OpenQualifiedStructure(1, object, "表")
	var services ui.Services
	tt := ui.NewTester(func(c *ui.Context) { services = c.Services(); s.Consume(services); s.View(c, d) }, 800, 650)
	tt.Frame()
	s.BeginDesign(d, services)
	waitSchema(t, tt, func() bool { return d.schemaDesign != nil && d.schemaDesign.loaded })
	t.Cleanup(s.CloseAll)
	return s, d, w, tt, services
}
func waitSchema(t *testing.T, tt *ui.Tester, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !done() && time.Now().Before(deadline) {
		tt.Frame()
		time.Sleep(time.Millisecond)
	}
	if !done() {
		t.Fatal("schema worker did not settle")
	}
	tt.Frame()
}
func TestSQLiteDesignerFormsKeepFieldIdentityAndNameOnlyRenamePreservesDefinition(t *testing.T) {
	s, d, _, tt, _ := schemaTestState(t)
	if err := s.schemaSelectRow(d, 0, 1); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	id, raw := d.schemaDesign.draft.Clauses[1].ID, d.schemaDesign.draft.Clauses[1].SQL
	if err := tt.Click("字段名称"); err != nil {
		t.Fatal(err)
	}
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Type("label")
	if !d.DesignDirty() {
		t.Fatal("pending field name not dirty")
	}
	if err := tt.Click("应用定义修改"); err != nil {
		t.Fatal(err)
	}
	c := d.schemaDesign.draft.Clauses[1]
	if c.ID != id || c.SQL != raw || c.Name != "label" {
		t.Fatalf("rename changed attributes/identity: %+v", c)
	}
	if err := tt.Click("SQL 预览"); err != nil {
		t.Fatal(err)
	}
	if d.schemaDesign.plan.Mode != "rename_column" || d.DesignBusy() {
		t.Fatal("name-only form did not stage native rename")
	}
}
func TestSQLiteDesignerStalePreviewAndFieldDropConfirmationCannotExecute(t *testing.T) {
	s, d, _, tt, services := schemaTestState(t)
	if err := tt.Click("添加字段"); err != nil {
		t.Fatal(err)
	}
	if err := s.PrepareSchemaDesign(d); err != nil {
		t.Fatal(err)
	}
	d.schemaDesign.field.Type = "BLOB"
	s.executeSchemaDesign(d, services)
	if d.DesignBusy() || !strings.Contains(d.err, "预览后") {
		t.Fatal("old preview acquired new form permissions")
	}
	d.schemaDesign.field = d.schemaDesign.fieldBase
	d.schemaDesign.draft = d.schemaDesign.saved.Clone()
	d.schemaDesign.draft.Clauses = d.schemaDesign.draft.Clauses[:1]
	if err := s.PrepareSchemaDesign(d); err != nil {
		t.Fatal(err)
	}
	s.executeSchemaDesign(d, services)
	if d.DesignBusy() || !strings.Contains(d.err, "确认删除字段") {
		t.Fatal("field deletion escaped confirmation")
	}
}
func TestSQLiteDesignerCancelledCloseDropsReceiptAndPreservesIndependentWorkerRequest(t *testing.T) {
	s, d, _, tt, services := schemaTestState(t)
	started, release := make(chan contract.SchemaDesignRequest, 1), make(chan struct{})
	original := s.deps.SchemaDesignFor
	s.deps.SchemaDesignFor = func(id int64) contract.SchemaDesignOperations {
		ops := original(id)
		ops.Apply = func(ctx context.Context, r contract.SchemaDesignRequest) contract.SchemaDesignOutcome {
			started <- r
			<-release
			return contract.SchemaDesignOutcome{State: "rolled_back", Error: ctx.Err()}
		}
		return ops
	}
	if err := tt.Click("添加字段"); err != nil {
		t.Fatal(err)
	}
	if err := s.PrepareSchemaDesign(d); err != nil {
		t.Fatal(err)
	}
	s.executeSchemaDesign(d, services)
	var request contract.SchemaDesignRequest
	select {
	case request = <-started:
	case <-time.After(time.Second):
		t.Fatal("worker did not start")
	}
	d.schemaDesign.draft.Clauses[0].Name = "new UI mutation"
	if request.Desired.Clauses[0].Name != "id" {
		t.Fatal("worker shared mutable draft")
	}
	s.cancelSchemaDesign(d)
	if !d.DesignBusy() || !d.schemaDesign.cancelRequested {
		t.Fatal("cancel released operation before acknowledgement")
	}
	s.Close(d)
	close(release)
	tt.Frame()
	if d.schemaDesign != nil || s.documents[d.id] != nil {
		t.Fatal("closed document accepted old receipt")
	}
}
func TestSQLiteDesignerParkedReceiptWaitsAndGenerationMismatchRetainsDraft(t *testing.T) {
	s, d, w, tt, services := schemaTestState(t)
	started, release := make(chan struct{}), make(chan struct{})
	original := s.deps.SchemaDesignFor
	s.deps.SchemaDesignFor = func(id int64) contract.SchemaDesignOperations {
		ops := original(id)
		ops.Apply = func(_ context.Context, r contract.SchemaDesignRequest) contract.SchemaDesignOutcome {
			close(started)
			<-release
			return contract.SchemaDesignOutcome{State: "rolled_back", Error: errors.New("fixture rollback")}
		}
		return ops
	}
	if err := tt.Click("添加字段"); err != nil {
		t.Fatal(err)
	}
	if err := s.PrepareSchemaDesign(d); err != nil {
		t.Fatal(err)
	}
	s.executeSchemaDesign(d, services)
	<-started
	w.ProfileID = 84
	close(release)
	tt.Frame()
	if !d.DesignBusy() {
		t.Fatal("parked receipt changed another profile UI")
	}
	w.ProfileID = 83
	w.Generation++
	waitSchema(t, tt, func() bool { return !d.DesignBusy() })
	if !d.DesignDirty() || !d.schemaDesign.blocked || !strings.Contains(d.err, "会话已失效") {
		t.Fatal("stale completion replaced draft")
	}
}
func TestSQLiteDesignerNarrowFormAndGuardKeepUnappliedText(t *testing.T) {
	s, d, _, tt, _ := schemaTestState(t)
	if err := tt.Click("添加字段"); err != nil {
		t.Fatal(err)
	}
	tt.SetSize(530, 430)
	tt.Frame()
	button, ok := tt.Find("应用定义修改")
	if !ok || button.Y < 0 || button.Y+button.H > 430 || button.X+button.W > 530 {
		t.Fatalf("apply clipped: %+v %v", button, ok)
	}
	if err := tt.Click("字段名称"); err != nil {
		t.Fatal(err)
	}
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Type("pending_name")
	if !s.GuardDesignLeave(d, func(ui.Services) { t.Fatal("guard continued before explicit choice") }) {
		t.Fatal("form bypassed guard")
	}
	tt.Frame()
	if err := tt.Click("继续编辑"); err != nil {
		t.Fatal(err)
	}
	if d.schemaDesign.field.Name != "pending_name" || !d.DesignDirty() {
		t.Fatal("guard lost pending form")
	}
}
func TestSQLiteDesignerCapabilityChecksDisableReadOnlyAndMissingBindings(t *testing.T) {
	s, d, w, _, _ := schemaTestState(t)
	if !s.CanCreateTable(83, "main") || !s.CanEditDesign(d) {
		t.Fatal("native feature was not exposed")
	}
	original := s.deps.SchemaDesignFor
	s.deps.SchemaDesignFor = func(id int64) contract.SchemaDesignOperations {
		return contract.SchemaDesignOperations{Snapshot: original(id).Snapshot}
	}
	if s.CanCreateTable(83, "main") || s.CanEditDesign(d) || s.CanRenameTable(83, "main", "t") {
		t.Fatal("a complete read-only baseline enabled graphical mutation")
	}
	s.deps.SchemaDesignFor = func(id int64) contract.SchemaDesignOperations {
		ops := original(id)
		ops.CanApply = func(contract.SchemaDesignTarget) bool { return false }
		return ops
	}
	if s.CanCreateTable(83, "main") || s.CanEditDesign(d) {
		t.Fatal("read-only feature enabled")
	}
	s.deps.SchemaDesignFor = nil
	if s.CanCreateTable(83, "main") || s.CanEditDesign(d) {
		t.Fatal("absent provider enabled")
	}
	w.Connected = false
	if s.CanCreateTable(83, "main") {
		t.Fatal("offline new table enabled")
	}
}

func TestSQLiteDesignerVerifiedRenameRefreshesInspectorAndRevokesOldRead(t *testing.T) {
	s, d, _, tt, services := schemaTestState(t)
	started, release := make(chan string, 2), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	s.Loader = func(_ context.Context, _ int64, database, table string) (*contract.TableStructure, error) {
		started <- database + "." + table
		if table == "t" {
			<-release // A late server read may finish even after cancellation.
		}
		return &contract.TableStructure{Columns: []contract.StructureColumn{{Name: table}}}, nil
	}
	s.Load(d, services.Invalidate, true)
	oldResults := d.results
	select {
	case target := <-started:
		if target != "main.t" {
			t.Fatal(target)
		}
	case <-time.After(time.Second):
		t.Fatal("old inspector read did not start")
	}
	s.BeginSchemaRename(d, "renamed", services)
	if err := s.PrepareSchemaDesign(d); err != nil {
		t.Fatal(err)
	}
	s.executeSchemaDesign(d, services)
	waitSchema(t, tt, func() bool { return !d.DesignBusy() && !d.Loading() })
	select {
	case target := <-started:
		if target != "main.renamed" {
			t.Fatal("inspector reloaded the old identity:", target)
		}
	case <-time.After(time.Second):
		t.Fatal("verified design did not refresh inspector")
	}
	if d.Error() != "" || d.Data() == nil || d.Data().Columns[0].Name != "renamed" {
		t.Fatalf("new inspector receipt: %+v %s", d.Data(), d.Error())
	}
	close(release)
	select {
	case <-oldResults:
	case <-time.After(time.Second):
		t.Fatal("revoked inspector worker did not settle")
	}
	tt.Frame()
	if d.Data().Columns[0].Name != "renamed" {
		t.Fatal("late inspector replaced the verified identity")
	}
}

func TestDesignerInvalidCommittedReceiptRetainsOriginalDraftAndBlocksReplay(t *testing.T) {
	s, d, _, tt, services := schemaTestState(t)
	original := d.Object()
	var received SchemaDesignEvent
	s.deps.SchemaDesigned = func(_ ui.Services, event SchemaDesignEvent) { received = event }
	binding := s.deps.SchemaDesignFor
	s.deps.SchemaDesignFor = func(id int64) contract.SchemaDesignOperations {
		ops := binding(id)
		ops.Apply = func(_ context.Context, r contract.SchemaDesignRequest) contract.SchemaDesignOutcome {
			actual := r.Original.Clone()
			actual.Schema.Table = r.Desired.Clone()
			actual.Target.Object.ProfileID++
			return contract.SchemaDesignOutcome{State: "committed", Verified: true, Actual: &actual}
		}
		return ops
	}
	s.schemaAdd(d)
	if err := s.PrepareSchemaDesign(d); err != nil {
		t.Fatal(err)
	}
	s.executeSchemaDesign(d, services)
	waitSchema(t, tt, func() bool { return !d.DesignBusy() })
	if d.Object() != original || !d.DesignDirty() || !d.schemaDesign.blocked || received.Outcome.Verified || received.Outcome.Actual != nil || received.Original.Object != original {
		t.Fatal("invalid commit replaced or replayed the draft", d.Error(), received)
	}
	if err := s.PrepareSchemaDesign(d); err == nil {
		t.Fatal("invalid commit could be replayed")
	}
	s.Close(d)
	s.Close(d)
}
