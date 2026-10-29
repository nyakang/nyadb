package shell

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

// The fake server keeps persisted bytes separate from the page and the UI
// draft. Tests can therefore distinguish typing, staging, and a write request.
type mysqlEditingFixture struct {
	mu      sync.Mutex
	page    *mysqlsvc.MySQLDataPage
	writes  []mysqlsvc.MySQLDataEdit
	failure error
}

func cloneEditingPage(p *mysqlsvc.MySQLDataPage) *mysqlsvc.MySQLDataPage {
	copy := *p
	copy.Rows = make([][]mysqlsvc.MySQLDataCell, len(p.Rows))
	for i := range p.Rows {
		copy.Rows[i] = append([]mysqlsvc.MySQLDataCell(nil), p.Rows[i]...)
	}
	copy.RowRefs = append([]*mysqlsvc.MySQLRowRef(nil), p.RowRefs...)
	copy.RowSnapshots = append([]*mysqlsvc.MySQLRowSnapshot(nil), p.RowSnapshots...)
	return &copy
}

func editingTestWorkspace(t *testing.T) (*State, *tab, *ui.Tester, *mysqlEditingFixture) {
	t.Helper()
	p := &mysqlsvc.MySQLDataPage{Database: "edit_test", Table: "records", Limit: 100, StableOrder: true,
		Columns: []string{"id", "note", "extra"}, ColumnKinds: []string{"bigint", "varchar", "varchar"},
		ColumnMetadata: []mysqlsvc.MySQLStructureColumn{{Name: "id", DataType: "bigint", Type: "bigint", Key: "PRI", Extra: "auto_increment"}, {Name: "note", DataType: "varchar", Type: "varchar(80)", Nullable: true}, {Name: "extra", DataType: "varchar", Type: "varchar(80)", Nullable: true}},
		Rows:           [][]mysqlsvc.MySQLDataCell{{{Text: "41"}, {Text: "alpha"}, {Text: "same-row"}}, {{Text: "42"}, {Text: "beta"}, {Null: true}}, {{Text: "43"}, {Text: "gamma"}, {Text: ""}}},
	}
	for i := range p.Rows {
		ref := &mysqlsvc.MySQLRowRef{Database: p.Database, Table: p.Table, Columns: []string{"id"}, Values: [][]byte{[]byte(p.Rows[i][0].Text)}}
		p.RowRefs = append(p.RowRefs, ref)
		hash := fmt.Sprintf("snapshot-%d", i)
		p.RowSnapshots = append(p.RowSnapshots, &mysqlsvc.MySQLRowSnapshot{Ref: *ref, Columns: p.Columns, Fingerprints: []*string{&hash, &hash, &hash}, Metadata: "fixture"})
	}
	fixture := &mysqlEditingFixture{page: cloneEditingPage(p)}
	s := New()
	s.inspectorOn = false
	s.mysqlSessionProfileID = 19
	s.mysqlSessionStatus = "已连接"
	s.mysqlSessionGeneration = 7
	doc := &tab{id: 2, kind: tabMySQLData, title: "records · 数据", dataProfileID: 19, dataDatabase: p.Database, dataTable: p.Table, dataPage: p, dataLimit: 100, dataSelectedRow: -1, dataHiddenColumns: map[string]bool{}}
	for _, name := range p.Columns {
		doc.dataColumns = append(doc.dataColumns, ui.TableColumn{ID: name, Title: name, Width: 150, MinWidth: 65})
	}
	s.tabs = append(s.tabs, doc)
	s.activeTab = 1
	s.mysqlDataAdvancedLoader = func(_ context.Context, _ int64, _, _ string, o mysqlsvc.MySQLDataOptions) (*mysqlsvc.MySQLDataPage, error) {
		fixture.mu.Lock()
		defer fixture.mu.Unlock()
		page := cloneEditingPage(fixture.page)
		page.Offset = o.Offset
		page.Limit = o.Limit
		return page, nil
	}
	s.mysqlDataSaver = func(_ context.Context, _ int64, _, _ string, edits []mysqlsvc.MySQLDataEdit) (*mysqlsvc.MySQLDataEditResult, error) {
		fixture.mu.Lock()
		defer fixture.mu.Unlock()
		fixture.writes = append(fixture.writes, edits...)
		if fixture.failure != nil {
			return nil, fixture.failure
		}
		result := &mysqlsvc.MySQLDataEditResult{}
		for _, edit := range edits {
			outcome := mysqlsvc.MySQLDataEditOutcome{Kind: edit.Kind, Original: edit.Original}
			if edit.Kind == "update" {
				for row, ref := range fixture.page.RowRefs {
					if string(ref.Values[0]) != string(edit.Original.Ref.Values[0]) {
						continue
					}
					for name, value := range edit.Values {
						col := slices.Index(fixture.page.Columns, name)
						fixture.page.Rows[row][col] = mysqlsvc.MySQLDataCell{Text: value.Text, Null: value.Null}
					}
					outcome.Saved = fixture.page.RowSnapshots[row]
					outcome.Previews = append([]mysqlsvc.MySQLDataCell(nil), fixture.page.Rows[row]...)
				}
			}
			if edit.Kind == "insert" {
				id := fmt.Sprint(44 + len(fixture.page.Rows) - 3)
				ref := &mysqlsvc.MySQLRowRef{Database: fixture.page.Database, Table: fixture.page.Table, Columns: []string{"id"}, Values: [][]byte{[]byte(id)}}
				line := make([]mysqlsvc.MySQLDataCell, len(fixture.page.Columns))
				line[0].Text = id
				for col, name := range fixture.page.Columns[1:] {
					if v, ok := edit.Values[name]; ok {
						line[col+1] = mysqlsvc.MySQLDataCell{Text: v.Text, Null: v.Null}
					} else {
						line[col+1].Null = true
					}
				}
				snapshot := &mysqlsvc.MySQLRowSnapshot{Ref: *ref, Columns: fixture.page.Columns, Metadata: "fixture"}
				fixture.page.Rows = append(fixture.page.Rows, line)
				fixture.page.RowRefs = append(fixture.page.RowRefs, ref)
				fixture.page.RowSnapshots = append(fixture.page.RowSnapshots, snapshot)
				outcome.GeneratedID = 44
				outcome.Saved = snapshot
				outcome.Previews = append([]mysqlsvc.MySQLDataCell(nil), line...)
			}
			if edit.Kind == "delete" {
				for row, ref := range fixture.page.RowRefs {
					if string(ref.Values[0]) == string(edit.Original.Ref.Values[0]) {
						fixture.page.Rows = slices.Delete(fixture.page.Rows, row, row+1)
						fixture.page.RowRefs = slices.Delete(fixture.page.RowRefs, row, row+1)
						fixture.page.RowSnapshots = slices.Delete(fixture.page.RowSnapshots, row, row+1)
						break
					}
				}
			}
			result.Outcomes = append(result.Outcomes, outcome)
		}
		return result, nil
	}
	tt := ui.NewTester(s.View, 1000, 650)
	tt.Frame()
	return s, doc, tt, fixture
}

func (f *mysqlEditingFixture) writeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.writes)
}
func (f *mysqlEditingFixture) cell(row, col int) mysqlsvc.MySQLDataCell {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.page.Rows[row][col]
}
func waitEditing(t *testing.T, s *State, doc *tab, tt *ui.Tester) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for doc.dataEdit.Busy || doc.dataLoading {
		if time.Now().After(deadline) {
			t.Fatal("editing operation did not finish")
		}
		time.Sleep(time.Millisecond)
		tt.Frame()
	}
}
func copyFocusedText(tt *ui.Tester) string {
	tt.Command("selectAll")
	tt.Command("copy")
	return tt.Clipboard()
}

func TestMySQLDataSingleClickTextMenuPasteUndoAndEscape(t *testing.T) {
	_, doc, tt, f := editingTestWorkspace(t)
	cell, ok := tt.Find("alpha")
	if !ok {
		t.Fatal("missing fixture cell")
	}
	if err := tt.Click("alpha"); err != nil {
		t.Fatal(err)
	}
	if doc.dataEdit.Editor == nil {
		t.Fatal("first click did not begin editing")
	}
	if got := copyFocusedText(tt); got != "alpha" {
		t.Fatalf("editor focus/selection %q", got)
	}
	tt.SetClipboard("中文😀")
	tt.Key(ui.Cmd, ui.KeyV)
	if got := copyFocusedText(tt); got != "中文😀" {
		t.Fatalf("paste changed wrong target: %q", got)
	}
	tt.Command("undo")
	if got := copyFocusedText(tt); got != "alpha" {
		t.Fatalf("text undo failed: %q", got)
	}
	tt.RightClickAt(cell.X+cell.W/2, cell.Y+cell.H/2)
	menu := tt.Menu()
	for _, label := range []string{"设置为 NULL", "添加记录", "删除记录", "复制字段名称"} {
		if slices.Contains(menu, label) {
			t.Fatalf("record command leaked into text menu: %v", menu)
		}
	}
	if len(menu) < 4 {
		t.Fatalf("missing native text menu: %v", menu)
	}
	tt.CloseMenu()
	tt.Key(0, ui.KeyEscape)
	if doc.dataEdit.Editor != nil || f.writeCount() != 0 || mysqlDataHasDraft(doc) {
		t.Fatal("Escape wrote data or retained reverted text")
	}
	if err := tt.RightClick("alpha"); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(tt.Menu(), "设置为 NULL") {
		t.Fatalf("record menu did not return: %v", tt.Menu())
	}
}

func TestMySQLDataEnterTabBlurAndDefaultRowLeaveCommit(t *testing.T) {
	s, doc, tt, f := editingTestWorkspace(t)
	if err := tt.Click("alpha"); err != nil {
		t.Fatal(err)
	}
	tt.Type("edited41")
	tt.Key(0, ui.KeyTab)
	if f.writeCount() != 0 || f.cell(0, 1).Text != "alpha" {
		t.Fatal("Tab wrote current record")
	}
	tt.Key(0, ui.KeyEnter)
	if f.writeCount() != 0 {
		t.Fatal("Enter wrote current record")
	}
	if err := tt.Click("查询 SQL 操作"); err != nil {
		t.Fatal(err)
	}
	tt.CloseMenu()
	if f.writeCount() != 0 {
		t.Fatal("toolbar blur wrote current record")
	}
	if err := tt.Click("beta"); err != nil {
		t.Fatal(err)
	}
	waitEditing(t, s, doc, tt)
	if f.writeCount() != 1 || f.cell(0, 1).Text != "edited41" {
		t.Fatalf("row leave did not persist draft: writes=%d value=%q", f.writeCount(), f.cell(0, 1).Text)
	}
}

func TestMySQLDataBatchPreservesDraftAcrossRowsAndCancel(t *testing.T) {
	_, doc, tt, f := editingTestWorkspace(t)
	doc.dataEdit.Batch = true
	tt.Frame()
	if err := tt.Click("alpha"); err != nil {
		t.Fatal(err)
	}
	tt.Type("draft41")
	tt.Key(0, ui.KeyEnter)
	if err := tt.Click("beta"); err != nil {
		t.Fatal(err)
	}
	tt.Type("draft42")
	tt.Key(0, ui.KeyEnter)
	if f.writeCount() != 0 || !mysqlDataHasDraft(doc) {
		t.Fatal("batch row change wrote data or lost drafts")
	}
	if err := tt.Click("取消数据更改"); err != nil {
		t.Fatal(err)
	}
	if mysqlDataHasDraft(doc) || f.writeCount() != 0 || !tt.HasText("alpha") || !tt.HasText("beta") {
		t.Fatal("cancel did not restore original page without writing")
	}
}

func TestMySQLDataNullAndEmptyStringStayDistinct(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value mysqlsvc.MySQLDataValue
	}{{"NULL", mysqlsvc.MySQLDataValue{Null: true}}, {"empty", mysqlsvc.MySQLDataValue{Text: ""}}} {
		t.Run(tc.name, func(t *testing.T) {
			s, doc, tt, f := editingTestWorkspace(t)
			doc.dataEdit.Batch = true
			s.setMySQLDataCell(ui.Services{}, doc, 0, 1, tc.value)
			tt.Frame()
			if !mysqlDataHasDraft(doc) {
				t.Fatal("value change was not staged")
			}
			if err := tt.Click("保存数据更改"); err != nil {
				t.Fatal(err)
			}
			waitEditing(t, s, doc, tt)
			if f.writeCount() != 1 || f.cell(0, 1).Null != tc.value.Null || f.cell(0, 1).Text != "" {
				t.Fatalf("NULL/empty changed: %+v", f.cell(0, 1))
			}
		})
	}
}

func TestMySQLDataFailedSaveRetainsDraftAndBlocksDuplicate(t *testing.T) {
	s, doc, tt, f := editingTestWorkspace(t)
	f.failure = errors.New("constraint rejected")
	doc.dataEdit.Batch = true
	if err := tt.Click("alpha"); err != nil {
		t.Fatal(err)
	}
	tt.Type("rejected")
	tt.Key(0, ui.KeyEnter)
	if err := tt.Click("保存数据更改"); err != nil {
		t.Fatal(err)
	}
	waitEditing(t, s, doc, tt)
	if f.writeCount() != 1 || !mysqlDataHasDraft(doc) || doc.dataEdit.Err == "" || f.cell(0, 1).Text != "alpha" {
		t.Fatal("failed save lost draft or mutated persisted data")
	}
	if err := tt.Click("取消数据更改"); err != nil {
		t.Fatal(err)
	}
	if mysqlDataHasDraft(doc) {
		t.Fatal("failed draft could not be discarded")
	}
}

func TestMySQLDataIMEKeysDoNotNavigateSaveOrCancel(t *testing.T) {
	_, doc, tt, f := editingTestWorkspace(t)
	if err := tt.Click("alpha"); err != nil {
		t.Fatal(err)
	}
	tt.Compose("zhong", 5)
	for _, key := range []ui.Key{ui.KeyEnter, ui.KeyTab, ui.KeyEscape} {
		tt.Key(0, key)
		if doc.dataEdit.Editor == nil || f.writeCount() != 0 {
			t.Fatalf("IME key %v escaped the editor", key)
		}
	}
	tt.Compose("", 0)
	tt.Type("中文")
	tt.Key(0, ui.KeyEnter)
	if doc.dataEdit.Editor != nil || !mysqlDataHasDraft(doc) || f.writeCount() != 0 {
		t.Fatal("committed IME text did not remain a draft")
	}
}

func TestMySQLDataModifiedClickDragAndRowHeaderRemainSelection(t *testing.T) {
	_, doc, tt, f := editingTestWorkspace(t)
	if err := tt.Click("41"); err != nil {
		t.Fatal(err)
	}
	tt.Key(0, ui.KeyEscape)
	if err := tt.ClickWith(ui.Shift, "beta"); err != nil {
		t.Fatal(err)
	}
	if doc.dataEdit.Editor != nil || doc.dataSelection.Len() != 2 || doc.dataState.Cells.Cursor != "note" || doc.dataState.Cells.Anchor != "id" {
		t.Fatalf("Shift selection became editor or wrong rectangle: cells=%+v rows=%d editor=%+v", doc.dataState.Cells, doc.dataSelection.Len(), doc.dataEdit.Editor)
	}
	tt.Key(ui.Cmd, ui.KeyC)
	if got := tt.Clipboard(); got != "41\talpha\n42\tbeta" {
		t.Fatalf("selected copy changed: %q", got)
	}
	if err := tt.RightClick("alpha"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("复制"); err != nil {
		t.Fatal(err)
	}
	if tt.Clipboard() != "41\talpha\n42\tbeta" || doc.dataSelection.Len() != 2 {
		t.Fatal("inside right click collapsed existing selection")
	}
	a, _ := tt.Find("alpha")
	b, _ := tt.Find("beta")
	tt.Press(a.X+a.W/2, a.Y+a.H/2)
	tt.Move(b.X+b.W/2, b.Y+b.H/2)
	tt.Release(b.X+b.W/2, b.Y+b.H/2)
	if doc.dataEdit.Editor != nil || doc.dataSelection.Len() != 2 || f.writeCount() != 0 {
		t.Fatal("drag selection entered editor or wrote data")
	}
	doc.dataLayout.showRowNumbers = true
	tt.Frame()
	if err := tt.Click("2"); err != nil {
		t.Fatal(err)
	}
	if doc.dataEdit.Editor != nil || !doc.dataState.Cells.All || doc.dataSelectedRow != 1 {
		t.Fatal("row header did not select whole row")
	}
}

func TestMySQLDataEditorKeepsIdentityAcrossReorderHiddenFieldsAndScroll(t *testing.T) {
	s, doc, tt, f := editingTestWorkspace(t)
	doc.dataEdit.Batch = true
	for row := 3; row < 93; row++ {
		id := fmt.Sprint(41 + row)
		ref := &mysqlsvc.MySQLRowRef{Database: doc.dataDatabase, Table: doc.dataTable, Columns: []string{"id"}, Values: [][]byte{[]byte(id)}}
		doc.dataPage.Rows = append(doc.dataPage.Rows, []mysqlsvc.MySQLDataCell{{Text: id}, {Text: "offscreen-" + id}, {Text: ""}})
		doc.dataPage.RowRefs = append(doc.dataPage.RowRefs, ref)
		doc.dataPage.RowSnapshots = append(doc.dataPage.RowSnapshots, &mysqlsvc.MySQLRowSnapshot{Ref: *ref, Columns: doc.dataPage.Columns, Metadata: "fixture"})
	}
	tt.Frame()
	if err := tt.Click("alpha"); err != nil {
		t.Fatal(err)
	}
	tt.Type("identity41")
	doc.dataState.Columns.Order = []string{"extra", "note", "id"}
	doc.dataHiddenColumns["id"] = true
	tt.Frame()
	if doc.dataEdit.Editor == nil || doc.dataEdit.Editor.Field != "note" || doc.dataEdit.Editor.Row != 0 || copyFocusedText(tt) != "identity41" {
		t.Fatal("reorder/hidden field moved or lost editor")
	}
	doc.dataHiddenColumns["note"] = true
	tt.Frame()
	if doc.dataEdit.Editor == nil || doc.dataEdit.Editor.Text != "identity41" {
		t.Fatal("hiding edited field lost draft")
	}
	delete(doc.dataHiddenColumns, "note")
	doc.dataState.ScrollIntoView(90)
	tt.Frame()
	if tt.HasText("alpha") || doc.dataEdit.Editor == nil || doc.dataEdit.Editor.Text != "identity41" {
		t.Fatal("offscreen virtualization lost or moved editor")
	}
	doc.dataState.ScrollIntoView(0)
	tt.Frame()
	if copyFocusedText(tt) != "identity41" {
		t.Fatal("returning to edited row lost input focus/text")
	}
	tt.Key(0, ui.KeyEnter)
	if err := tt.Click("保存数据更改"); err != nil {
		t.Fatal(err)
	}
	waitEditing(t, s, doc, tt)
	if f.cell(0, 1).Text != "identity41" || f.cell(1, 1).Text != "beta" || f.cell(0, 2).Text != "same-row" {
		t.Fatal("editor wrote a different row or field after layout change")
	}
}

func TestMySQLDataLeavePromptCanCancelDiscardOrSave(t *testing.T) {
	for _, action := range []string{"cancel", "discard", "save"} {
		t.Run(action, func(t *testing.T) {
			s, doc, tt, f := editingTestWorkspace(t)
			doc.dataEdit.Batch = true
			if err := tt.Click("alpha"); err != nil {
				t.Fatal(err)
			}
			tt.Type("leave41")
			tt.Key(0, ui.KeyEnter)
			s.closeTab(1)
			tt.Frame()
			if len(s.tabs) != 2 || !doc.dataEdit.LeaveOpen || f.writeCount() != 0 {
				t.Fatal("close destroyed drafts without prompting")
			}
			switch action {
			case "cancel":
				// The reference gives Cancel the default focus.
				tt.Key(0, ui.KeyEnter)
				if len(s.tabs) != 2 || !mysqlDataHasDraft(doc) {
					t.Fatal("cancel leave discarded document")
				}
			case "discard":
				// The default modal action has focus. Keyboard navigation avoids
				// matching the same visible label in the background pending bar.
				tt.Key(ui.Shift, ui.KeyTab)
				tt.Key(0, ui.KeyEnter)
				if len(s.tabs) != 1 || f.writeCount() != 0 {
					t.Fatalf("discard leave wrote data or kept document: tabs=%d writes=%d draft=%v LeaveOpen=%v", len(s.tabs), f.writeCount(), mysqlDataHasDraft(doc), doc.dataEdit.LeaveOpen)
				}
			case "save":
				tt.Key(ui.Shift, ui.KeyTab)
				tt.Key(ui.Shift, ui.KeyTab)
				tt.Key(0, ui.KeyEnter)
				waitEditing(t, s, doc, tt)
				if len(s.tabs) != 1 || f.cell(0, 1).Text != "leave41" {
					t.Fatalf("save leave closed before persistence or failed to close: tabs=%d value=%q drafts=%v LeaveOpen=%v", len(s.tabs), f.cell(0, 1).Text, mysqlDataHasDraft(doc), doc.dataEdit.LeaveOpen)
				}
			}
		})
	}
}

func TestMySQLDataNoIdentityOrGeneratedColumnsCannotEdit(t *testing.T) {
	_, doc, tt, f := editingTestWorkspace(t)
	doc.dataPage.RowRefs[0] = nil
	doc.dataPage.RowSnapshots[0] = nil
	doc.dataPage.ColumnMetadata[2].Extra = "STORED GENERATED"
	tt.Frame()
	if err := tt.Click("alpha"); err != nil {
		t.Fatal(err)
	}
	if doc.dataEdit.Editor != nil {
		t.Fatal("row without unique identity entered editor")
	}
	if err := tt.Click("same-row"); err != nil {
		t.Fatal(err)
	}
	if doc.dataEdit.Editor != nil || f.writeCount() != 0 {
		t.Fatal("generated field entered editor")
	}
}

func TestMySQLDataExplicitCompleteReadRejectsChangedSnapshot(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(fmt.Sprint(conflict), func(t *testing.T) {
			s, doc, tt, _ := editingTestWorkspace(t)
			doc.dataPage.Rows[0][2] = mysqlsvc.MySQLDataCell{Text: "preview", Truncated: true}
			s.mysqlDataRowReader = func(_ context.Context, _ int64, _, _ string, _ mysqlsvc.MySQLRowRef) (*mysqlsvc.MySQLDataRow, error) {
				snapshot := *doc.dataPage.RowSnapshots[0]
				if conflict {
					snapshot.Metadata = "changed metadata"
				}
				return &mysqlsvc.MySQLDataRow{Columns: doc.dataPage.ColumnMetadata, Snapshot: &snapshot, Values: map[string]mysqlsvc.MySQLDataValue{"extra": {Text: "complete original value"}}}, nil
			}
			tt.Frame()
			if err := tt.Click("preview"); err != nil {
				t.Fatal(err)
			}
			if doc.dataEdit.Editor != nil {
				t.Fatal("truncated preview entered ordinary editor")
			}
			s.beginMySQLCellEdit(ui.Services{}, doc, 0, 2, true)
			waitEditing(t, s, doc, tt)
			if conflict {
				if doc.dataEdit.Editor != nil || doc.dataEdit.Err == "" {
					t.Fatal("changed complete read was silently accepted")
				}
			} else if doc.dataEdit.Editor == nil || doc.dataEdit.Editor.Text != "complete original value" || !doc.dataEdit.Editor.Dedicated {
				t.Fatal("complete value did not enter dedicated editor")
			}
		})
	}
}

func TestMySQLDataAddSaveDeleteAndCancelRecords(t *testing.T) {
	s, doc, tt, f := editingTestWorkspace(t)
	doc.dataEdit.Batch = true
	tt.Frame()
	if err := tt.Click("添加记录"); err != nil {
		t.Fatal(err)
	}
	if len(doc.dataPage.Rows) != 4 || doc.dataEdit.Editor == nil || doc.dataEdit.Editor.Field != "note" {
		t.Fatal("add did not create default-valued draft and focus first writable field")
	}
	tt.Type("inserted")
	tt.Key(0, ui.KeyEnter)
	if err := tt.Click("取消数据更改"); err != nil {
		t.Fatal(err)
	}
	if len(doc.dataPage.Rows) != 3 || f.writeCount() != 0 || mysqlDataHasDraft(doc) {
		t.Fatal("cancel inserted draft wrote or kept a new row")
	}
	if err := tt.Click("添加记录"); err != nil {
		t.Fatal(err)
	}
	tt.Type("inserted")
	tt.Key(0, ui.KeyEnter)
	if err := tt.Click("保存数据更改"); err != nil {
		t.Fatal(err)
	}
	waitEditing(t, s, doc, tt)
	if len(doc.dataPage.Rows) != 4 || doc.dataPage.Rows[3][0].Text != "44" || doc.dataPage.RowRefs[3] == nil || f.cell(3, 1).Text != "inserted" || mysqlDataHasDraft(doc) {
		t.Fatal("saved insertion did not recover generated row identity")
	}
	doc.dataSelectedRow = 3
	doc.dataSelection.Clear()
	doc.dataSelection.Add(3)
	tt.Frame()
	if err := tt.Click("删除记录"); err != nil {
		t.Fatal(err)
	}
	if f.writeCount() != 1 || !mysqlDataHasDraft(doc) {
		t.Fatal("batch delete wrote before save")
	}
	if err := tt.Click("取消数据更改"); err != nil {
		t.Fatal(err)
	}
	if len(doc.dataPage.Rows) != 4 || f.writeCount() != 1 {
		t.Fatal("cancel delete lost record or wrote")
	}
	doc.dataSelectedRow = 3
	doc.dataSelection.Clear()
	doc.dataSelection.Add(3)
	tt.Frame()
	if err := tt.Click("删除记录"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("保存数据更改"); err != nil {
		t.Fatal(err)
	}
	waitEditing(t, s, doc, tt)
	if len(doc.dataPage.Rows) != 3 || f.writeCount() != 2 || mysqlDataHasDraft(doc) {
		t.Fatal("selected record delete did not complete")
	}
}

func TestMySQLDataDeletingUncommittedInsertOnlyRemovesLocalDraft(t *testing.T) {
	s, doc, tt, f := editingTestWorkspace(t)
	doc.dataEdit.Batch = true
	s.addMySQLDataRecord(ui.Services{}, doc)
	tt.Frame()
	tt.Type("new draft")
	tt.Key(0, ui.KeyEnter)
	s.deleteMySQLDataRecords(ui.Services{}, doc, 3)
	tt.Frame()
	if len(doc.dataPage.Rows) != 3 || f.writeCount() != 0 || mysqlDataHasDraft(doc) {
		t.Fatal("deleting uncommitted insertion scheduled a database DELETE instead of removing draft")
	}
}

func TestMySQLDataCanceledReaderIgnoresLateFullValueResult(t *testing.T) {
	s, doc, tt, _ := editingTestWorkspace(t)
	started := make(chan struct{})
	release := make(chan struct{})
	s.mysqlDataRowReader = func(_ context.Context, _ int64, _, _ string, _ mysqlsvc.MySQLRowRef) (*mysqlsvc.MySQLDataRow, error) {
		close(started)
		<-release
		return &mysqlsvc.MySQLDataRow{Snapshot: doc.dataPage.RowSnapshots[0], Values: map[string]mysqlsvc.MySQLDataValue{"extra": {Text: "late secret"}}}, nil
	}
	s.beginMySQLCellEdit(ui.Services{}, doc, 0, 2, true)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("reader did not start")
	}
	s.cancelMySQLDataEdit(doc)
	close(release)
	for range 5 {
		time.Sleep(time.Millisecond)
		tt.Frame()
	}
	if doc.dataEdit.Editor != nil || doc.dataEdit.Busy || doc.dataPage.Rows[0][2].Text != "same-row" {
		t.Fatal("late canceled complete read changed editor or data")
	}
}

func TestMySQLDataBusySaveStopsWithoutDuplicateAndKeepsDraft(t *testing.T) {
	s, doc, tt, _ := editingTestWorkspace(t)
	started := make(chan struct{})
	calls := 0
	var mu sync.Mutex
	s.mysqlDataSaver = func(ctx context.Context, _ int64, _, _ string, _ []mysqlsvc.MySQLDataEdit) (*mysqlsvc.MySQLDataEditResult, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	doc.dataEdit.Batch = true
	if err := tt.Click("alpha"); err != nil {
		t.Fatal(err)
	}
	tt.Type("waiting")
	tt.Key(0, ui.KeyEnter)
	if err := tt.Click("保存数据更改"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("saver did not start")
	}
	s.saveMySQLData(ui.Services{}, doc)
	if !doc.dataEdit.Busy {
		t.Fatal("save did not remain busy while request was pending")
	}
	if err := tt.Click("停止数据操作"); err != nil {
		t.Fatal(err)
	}
	waitEditing(t, s, doc, tt)
	mu.Lock()
	count := calls
	mu.Unlock()
	if count != 1 || !mysqlDataHasDraft(doc) {
		t.Fatal("stop duplicated write or discarded draft")
	}
}

func TestMySQLDataPageRefreshSortAndPageSizeWaitForDraftDecision(t *testing.T) {
	for _, action := range []string{"page", "refresh", "sort", "size"} {
		t.Run(action, func(t *testing.T) {
			s, doc, tt, f := editingTestWorkspace(t)
			doc.dataEdit.Batch = true
			if err := tt.Click("alpha"); err != nil {
				t.Fatal(err)
			}
			tt.Type("pending")
			tt.Key(0, ui.KeyEnter)
			switch action {
			case "page":
				s.changeMySQLDataPage(ui.Services{}, doc, 100)
			case "refresh":
				s.resetAndReloadMySQLData(ui.Services{}, doc)
			case "sort":
				s.applyMySQLDataCellSort(ui.Services{}, doc, "note", true)
			case "size":
				s.changeMySQLDataPageSize(ui.Services{}, doc, 50)
			}
			tt.Frame()
			if !doc.dataEdit.LeaveOpen || doc.dataPage == nil || doc.dataOffset != 0 || doc.dataLimit != 100 || doc.dataSortColumn != "" || f.writeCount() != 0 {
				t.Fatal("navigation mutated page/query before draft decision")
			}
			if err := tt.Click("取消"); err != nil {
				t.Fatal(err)
			}
			if !mysqlDataHasDraft(doc) || doc.dataPage.Rows[0][1].Text != "pending" {
				t.Fatal("cancel navigation lost draft")
			}
		})
	}
}

func TestMySQLDataUnknownCommitCannotReplayOrDiscardUnverifiedDraft(t *testing.T) {
	s, doc, tt, _ := editingTestWorkspace(t)
	calls := 0
	var mu sync.Mutex
	s.mysqlDataSaver = func(_ context.Context, _ int64, _, _ string, edits []mysqlsvc.MySQLDataEdit) (*mysqlsvc.MySQLDataEditResult, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		return &mysqlsvc.MySQLDataEditResult{CommitUncertain: true, Outcomes: []mysqlsvc.MySQLDataEditOutcome{{Kind: "update", Original: edits[0].Original}}}, mysqlsvc.ErrMySQLDataCommitUncertain
	}
	doc.dataEdit.Batch = true
	if err := tt.Click("alpha"); err != nil {
		t.Fatal(err)
	}
	tt.Type("unknown")
	tt.Key(0, ui.KeyEnter)
	if err := tt.Click("保存数据更改"); err != nil {
		t.Fatal(err)
	}
	waitEditing(t, s, doc, tt)
	if doc.dataEdit.Uncertain == nil || !mysqlDataHasDraft(doc) {
		t.Fatal("uncertain acknowledgement hid pending result")
	}
	s.saveMySQLData(ui.Services{}, doc)
	s.discardMySQLData(doc)
	mu.Lock()
	count := calls
	mu.Unlock()
	if count != 1 || !mysqlDataHasDraft(doc) {
		t.Fatal("uncertain write replayed or disappeared")
	}
}

func TestMySQLDataDisconnectAndWorkbenchCloseWaitForDraftDecision(t *testing.T) {
	for _, action := range []string{"disconnect", "workbench"} {
		t.Run(action, func(t *testing.T) {
			s, doc, tt, f := editingTestWorkspace(t)
			doc.dataEdit.Batch = true
			if err := tt.Click("alpha"); err != nil {
				t.Fatal(err)
			}
			tt.Type("pending close")
			tt.Key(0, ui.KeyEnter)
			closed := false
			if action == "disconnect" {
				s.disconnectMySQLProfile(19)
			} else if !s.RequestWorkbenchClose(func() { closed = true }) {
				t.Fatal("workbench close was not vetoed")
			}
			tt.Frame()
			if !doc.dataEdit.LeaveOpen || s.mysqlSessionStatus != "已连接" || len(s.tabs) != 2 || closed || f.writeCount() != 0 {
				t.Fatal("close disconnected or destroyed draft before decision")
			}
			if err := tt.Click("取消"); err != nil {
				t.Fatal(err)
			}
			if !mysqlDataHasDraft(doc) || closed || s.mysqlSessionStatus != "已连接" {
				t.Fatal("cancel close lost draft/session")
			}
		})
	}
}

func TestMySQLDataParkedWriteResultIsKeptUntilItsProfileReturns(t *testing.T) {
	s, doc, tt, _ := editingTestWorkspace(t)
	doc.dataEdit.Batch = true
	if err := tt.Click("alpha"); err != nil {
		t.Fatal(err)
	}
	tt.Type("parked")
	tt.Key(0, ui.KeyEnter)
	keys := []string{mysqlDataRowKey(doc, 0)}
	result := make(chan mysqlEditResult, 1)
	doc.dataEdit.Results = result
	doc.dataEdit.Request = 8
	doc.dataEdit.Busy = true
	s.mysqlSessionProfileID = 20
	result <- mysqlEditResult{Generation: 7, Request: 8, Profile: 19, Keys: keys, Saved: &mysqlsvc.MySQLDataEditResult{Outcomes: []mysqlsvc.MySQLDataEditOutcome{{Kind: "update", Saved: doc.dataPage.RowSnapshots[0], Previews: append([]mysqlsvc.MySQLDataCell(nil), doc.dataPage.Rows[0]...)}}}}
	tt.Frame()
	if !doc.dataEdit.Busy || len(result) != 1 || !mysqlDataHasDraft(doc) {
		t.Fatal("other profile consumed or lost pending write result")
	}
	s.mysqlSessionProfileID = 19
	tt.Frame()
	if doc.dataEdit.Busy || doc.dataEdit.Results != nil || mysqlDataHasDraft(doc) {
		t.Fatal("profile return did not consume its own successful result")
	}
}

func TestMySQLDataSavedIdentityKeepsBoundedTypedPreview(t *testing.T) {
	for _, tc := range []struct {
		name, kind string
		raw        []byte
		preview    mysqlsvc.MySQLDataCell
	}{
		{"binary", "varbinary", []byte{0, 0xff, 1}, mysqlsvc.MySQLDataCell{Text: "0x00ff01"}},
		{"long text", "varchar", []byte(strings.Repeat("长", 1000)), mysqlDataValuePreview(mysqlsvc.MySQLDataValue{Text: strings.Repeat("长", 1000)})},
	} {
		for _, serverPreview := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/server_preview=%t", tc.name, serverPreview), func(t *testing.T) {
				s, doc, tt, _ := editingTestWorkspace(t)
				doc.dataPage.ColumnMetadata[0].DataType = tc.kind
				doc.dataPage.ColumnKinds[0] = tc.kind
				ref := &mysqlsvc.MySQLRowRef{Database: doc.dataDatabase, Table: doc.dataTable, Columns: []string{"id"}, Values: [][]byte{tc.raw}}
				saved := &mysqlsvc.MySQLRowSnapshot{Ref: *ref, Columns: doc.dataPage.Columns}
				key := mysqlDataRowKey(doc, 0)
				doc.dataEdit.Drafts = map[string]*mysqlRowDraft{key: {Key: key, Row: 0, Kind: "update", Values: map[string]mysqlsvc.MySQLDataValue{"note": {Text: "saved"}}}}
				outcome := mysqlsvc.MySQLDataEditOutcome{Kind: "update", Saved: saved}
				if serverPreview {
					outcome.PreviewColumns = []string{"id", "note"}
					outcome.Previews = []mysqlsvc.MySQLDataCell{tc.preview, {Text: "saved"}}
				}
				s.acceptMySQLDataSaved(doc, []string{key}, &mysqlsvc.MySQLDataEditResult{Outcomes: []mysqlsvc.MySQLDataEditOutcome{outcome}})
				tt.Frame()
				if doc.dataPage.Rows[0][0] != tc.preview {
					t.Fatalf("identity bytes replaced typed/bounded preview: %+v", doc.dataPage.Rows[0][0])
				}
				if string(doc.dataPage.RowRefs[0].Values[0]) != string(tc.raw) || mysqlDataHasDraft(doc) {
					t.Fatal("saved identity bytes or draft completion lost")
				}
			})
		}
	}
}

func TestMySQLDataRepeatedShiftKeepsAnchorAndCmdKeepsColumnBlock(t *testing.T) {
	_, doc, tt, _ := editingTestWorkspace(t)
	if err := tt.Click("41"); err != nil {
		t.Fatal(err)
	}
	tt.Key(0, ui.KeyEscape)
	if err := tt.ClickWith(ui.Shift, "gamma"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ClickWith(ui.Shift, "beta"); err != nil {
		t.Fatal(err)
	}
	if !doc.dataSelection.Has(0) || !doc.dataSelection.Has(1) || doc.dataSelection.Has(2) {
		t.Fatalf("repeated Shift moved row anchor: 0=%v 1=%v 2=%v", doc.dataSelection.Has(0), doc.dataSelection.Has(1), doc.dataSelection.Has(2))
	}
	if err := tt.ClickWith(ui.Cmd, "gamma"); err != nil {
		t.Fatal(err)
	}
	if doc.dataSelection.Len() != 3 || doc.dataState.Cells.Anchor != "id" || doc.dataState.Cells.Cursor != "note" || doc.dataEdit.Editor != nil {
		t.Fatalf("Cmd multirow selection changed existing column block: %+v", doc.dataState.Cells)
	}
	tt.Key(ui.Cmd, ui.KeyC)
	if got := tt.Clipboard(); got != "41\talpha\n42\tbeta\n43\tgamma" {
		t.Fatalf("multirow block copy %q", got)
	}
}

func TestMySQLDataCellPaddingClickAndNoOpValuesDoNotCreateDrafts(t *testing.T) {
	s, doc, tt, f := editingTestWorkspace(t)
	cell, ok := tt.Find("alpha")
	if !ok {
		t.Fatal("missing alpha")
	}
	// This point is in the 150-DIP field but outside its short text glyphs.
	tt.ClickAt(cell.X+110, cell.Y+cell.H/2)
	if doc.dataEdit.Editor == nil || doc.dataEdit.Editor.Field != "note" {
		t.Fatal("cell padding click only selected instead of beginning edit")
	}
	tt.Key(0, ui.KeyEscape)
	for _, tc := range []struct {
		row, col int
		v        mysqlsvc.MySQLDataValue
	}{{0, 1, mysqlsvc.MySQLDataValue{Text: "alpha"}}, {1, 2, mysqlsvc.MySQLDataValue{Null: true}}, {2, 2, mysqlsvc.MySQLDataValue{Text: ""}}} {
		s.setMySQLDataCell(ui.Services{}, doc, tc.row, tc.col, tc.v)
		tt.Frame()
		if mysqlDataHasDraft(doc) || f.writeCount() != 0 {
			t.Fatalf("same value created a draft row=%d col=%d", tc.row, tc.col)
		}
	}
}
