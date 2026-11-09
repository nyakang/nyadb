package dataworkspace

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func TestDataSaveAvailabilityPreservesSessionAndUncertainGuards(t *testing.T) {
	s, d, _, _ := editingTestWorkspace(t)
	d.edit.Batch = true
	s.setMySQLDataCell(ui.Services{}, d, 0, 1, contract.DataValue{Text: "pending"})
	if !s.CanSave(d) {
		t.Fatal("pending active document cannot save")
	}
	d.edit.Uncertain = &contract.DataEditResult{}
	if s.CanSave(d) {
		t.Fatal("uncertain commit offered replay")
	}
	d.edit.Uncertain = nil
	d.generation = s.session().Generation + 1
	if s.CanSave(d) {
		t.Fatal("stale session offered save")
	}
	d.generation = 0
	d.loading = true
	if s.CanSave(d) {
		t.Fatal("loading document offered save")
	}
}

func TestDataToolbarJSONLCapabilityDoesNotBypassExportScopeWizard(t *testing.T) {
	s, d, _, _ := editingTestWorkspace(t)
	d.capabilities.JSONL = true
	called := false
	s.savePath = func(string) (string, error) { called = true; return "", nil }
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.mysqlDataToolbar(c, d) }, 1500, 300)
	if err := tt.Click("导出"); err != nil {
		t.Fatal(err)
	}
	if called || !d.transfer.Open || !d.transfer.Wizard || d.transfer.Kind != "export" || d.transfer.Scope != "page" {
		t.Fatal("general export silently changed format or scope")
	}
}

func TestDraftUndoRedoOwnsBinaryValuesAndStopsAtCommit(t *testing.T) {
	s, d, tt, f := editingTestWorkspace(t)
	d.edit.Batch = true
	s.setMySQLDataCell(ui.Services{}, d, 0, 1, contract.DataValue{Text: "first"})
	raw := []byte{1, 2}
	s.setMySQLDataCell(ui.Services{}, d, 0, 2, contract.DataValue{Kind: contract.Binary, Binary: true, Bytes: raw})
	raw[0] = 9
	if !s.undoDraft(d, false) || d.page.Rows[0][2].Text != "same-row" || d.page.Rows[0][1].Text != "first" {
		t.Fatal("undo restored wrong preview")
	}
	if !s.undoDraft(d, false) || mysqlDataHasDraft(d) || d.page.Rows[0][1].Text != "alpha" {
		t.Fatal("undo did not restore clean draft")
	}
	if !s.undoDraft(d, true) || !s.undoDraft(d, true) {
		t.Fatal("redo unavailable")
	}
	draft := mysqlDataDraftForRow(d, 0)
	if draft.Values["extra"].Bytes[0] != 1 {
		t.Fatal("history aliases input bytes")
	}
	draft.Values["extra"].Bytes[0] = 8
	if !s.undoDraft(d, false) || !s.undoDraft(d, true) || mysqlDataDraftForRow(d, 0).Values["extra"].Bytes[0] != 1 {
		t.Fatal("restored draft aliases history")
	}
	d.edit.Uncertain = &contract.DataEditResult{}
	if s.undoDraft(d, false) || s.canDraftUndo(d, true) {
		t.Fatal("uncertain write allowed undo")
	}
	d.edit.Uncertain = nil
	s.saveMySQLData(ui.Services{}, d)
	waitEditing(t, s, d, tt)
	if len(d.edit.Undo) != 0 || len(d.edit.Redo) != 0 || s.undoDraft(d, false) || f.writeCount() == 0 {
		t.Fatal("committed write can be replayed by undo")
	}
}
func TestDraftUndoRestoresInsertDeleteAndClearsRedoAfterNewEdit(t *testing.T) {
	s, d, _, f := editingTestWorkspace(t)
	d.edit.Batch = true
	s.addMySQLDataRecord(ui.Services{}, d)
	s.finishMySQLCellEdit(d, false)
	if len(d.page.Rows) != 4 || !s.undoDraft(d, false) || len(d.page.Rows) != 3 {
		t.Fatal("insert undo failed")
	}
	if !s.undoDraft(d, true) || len(d.page.Rows) != 4 || mysqlDataDraftForRow(d, 3).Kind != "insert" {
		t.Fatal("insert redo failed")
	}
	s.discardMySQLData(d)
	d.selectedRow = 0
	d.selection.Add(0)
	s.deleteMySQLDataRecords(ui.Services{}, d, 0)
	if !s.undoDraft(d, false) || mysqlDataHasDraft(d) || len(d.page.Rows) != 3 {
		t.Fatal("delete undo failed")
	}
	s.setMySQLDataCell(ui.Services{}, d, 0, 1, contract.DataValue{Text: "new"})
	if s.canDraftUndo(d, true) || f.writeCount() != 0 {
		t.Fatal("new draft retained redo or performed write")
	}
}
func TestPageJumpValidatesOverflowAndUsesOffsetForNonAdjacentPage(t *testing.T) {
	for _, input := range []string{"0", "-1", "9223372036854775807", "abc"} {
		if _, e := dataPageOffset(input, 100); e == nil {
			t.Fatal("invalid page accepted", input)
		}
	}
	s, d, tt, _ := editingTestWorkspace(t)
	d.page.HasMore = true
	d.page.KeysetAvailable = true
	d.page.NextCursor = &contract.DataCursor{Columns: []string{"id"}, Values: [][]byte{[]byte("43")}}
	got := make(chan contract.DataOptions, 1)
	s.AdvancedLoader = func(_ context.Context, _ int64, _, _ string, o contract.DataOptions) (*contract.DataPage, error) {
		got <- o
		return &contract.DataPage{Columns: []string{"id"}}, nil
	}
	d.pageNumberDraft = "3"
	if !s.jumpDataPage(ui.Services{}, d) {
		t.Fatal(d.pageNumberError)
	}
	waitEditing(t, s, d, tt)
	o := <-got
	if o.Offset != 200 || o.UseKeyset || o.After != nil {
		t.Fatalf("non-adjacent jump reused seek cursor: %+v", o)
	}
}
func TestRecordFormReadsCompleteFieldAndSharesGridDraft(t *testing.T) {
	s, d, tt, f := editingTestWorkspace(t)
	d.edit.Batch = true
	d.recordFormMode = true
	d.selectedRow = 0
	d.page.Rows[0][2].Text = "preview"
	d.page.Rows[0][2].Truncated = true
	full := strings.Repeat("完整值", 900)
	s.RowReader = func(context.Context, int64, string, string, contract.RowRef) (*contract.DataRow, error) {
		return &contract.DataRow{Snapshot: d.page.RowSnapshots[0], Values: map[string]contract.DataValue{"extra": {Text: full}}}, nil
	}
	tt.Frame()
	if err := tt.Click("编辑记录字段 extra"); err != nil {
		t.Fatal(err)
	}
	waitEditing(t, s, d, tt)
	if d.edit.Editor == nil || d.edit.Editor.Text != full || f.writeCount() != 0 {
		t.Fatal("form used preview as full edit value")
	}
	d.edit.Editor.Text = full + "tail"
	if err := tt.Click("应用字段"); err != nil {
		t.Fatal(err)
	}
	if mysqlDataDraftForRow(d, 0).Values["extra"].Text != full+"tail" {
		t.Fatal("form did not share full draft")
	}
	d.recordFormMode = false
	tt.Frame()
	if !s.undoDraft(d, false) || mysqlDataHasDraft(d) {
		t.Fatal("form edits did not share undo history")
	}
}

func TestRecordFormLongPreviewsDoNotPaintOverOtherFields(t *testing.T) {
	s, d, _, _ := editingTestWorkspace(t)
	d.recordFormMode = true
	d.selectedRow = 0
	common := strings.Repeat("preview ", 75)
	d.page.Rows[0][1].Text = common + strings.Repeat("A", 600)
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.recordForm(c, d) }, 720, 520)
	tt.Frame()
	before := bytes.Clone(tt.Image().Pix)
	d.page.Rows[0][1].Text = common + strings.Repeat("B", 600)
	tt.Frame()
	if !bytes.Equal(before, tt.Image().Pix) {
		t.Fatal("text beyond the form preview painted over following fields")
	}
	if !tt.HasText("extra") {
		t.Fatal("long form preview hid the next field")
	}
}
func TestDisabledFilterRowsStayEditableWithoutReachingQuery(t *testing.T) {
	s, d, _, _ := editingTestWorkspace(t)
	valid := contract.DataFilterRule{Column: "note", Mode: "equal", Value: "first"}
	invalid := contract.DataFilterRule{Column: "removed", Mode: "equal", Value: "draft"}
	d.groupDraft = &contract.FilterExpression[contract.DataFilterRule]{Op: "or", Children: []contract.FilterExpression[contract.DataFilterRule]{{Op: "rule", Rule: &valid}, {Op: "rule", Rule: &invalid}}}
	d.disabledGroupRules = map[*contract.DataFilterRule]bool{&invalid: true}
	rules, ok := s.validateFilterGroups(d)
	if !ok || len(rules) != 1 || rules[0].Column != "note" {
		t.Fatal("disabled condition entered validation/query", d.ruleError)
	}
	effective := effectiveFilterGroup(d, d.groupDraft)
	if len(effective.Children) != 1 || effective.Op != "or" || invalid.Value != "draft" {
		t.Fatal("disabled draft or group relation lost")
	}
	d.disabledGroupRules[&invalid] = false
	if _, ok := s.validateFilterGroups(d); ok {
		t.Fatal("enabled invalid field accepted")
	}
}

func TestSwitchingToGroupedFiltersRetainsDisabledDraftAndInlineError(t *testing.T) {
	s, d, tt, _ := editingTestWorkspace(t)
	d.rulesExpanded = true
	d.filterColumnDraft = "note"
	d.filterModeDraft = "精确匹配"
	d.filterValueDraft = "kept"
	d.mainFilterDisabled = true
	d.filterExtraDraft = []mysqlDataRuleDraft{{Column: "removed", Mode: "精确匹配", Value: "invalid"}}
	d.ruleError = "condition 2"
	tt.Frame()
	if _, ok := tt.Find("条件错误 2"); !ok {
		t.Fatal("invalid simple condition has no nearby explanation")
	}
	s.beginFilterGroups(d)
	if len(d.groupDraft.Children) != 2 || !d.disabledGroupRules[d.groupDraft.Children[0].Rule] || d.groupDraft.Children[0].Rule.Value != "kept" {
		t.Fatal("group conversion lost disabled draft")
	}
}
