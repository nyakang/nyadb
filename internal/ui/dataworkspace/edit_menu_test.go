package dataworkspace

import (
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func TestMySQLDataSQLCopyPreservesIdentityAndRejectsIncompleteValues(t *testing.T) {
	doc := alignedDataTestTab()
	doc.page.Database, doc.page.Table = "d`b", "records"
	doc.grid.Cells.All = true
	doc.selectedRow = 0
	doc.page.ColumnKinds = []string{"bigint", "varchar"}
	doc.page.Rows[0][1].Text = "O'Reilly\\n中文"
	doc.page.RowRefs = []*mysqlsvc.MySQLRowRef{{Database: "d`b", Table: "records", Columns: []string{"id"}, Values: [][]byte{[]byte("1")}}}
	text, err := mysqlDataSQLSelection(doc, true)
	if err != nil || !strings.Contains(text, "UPDATE `d``b`.`records`") || !strings.Contains(text, " WHERE CAST(`id` AS BINARY) = X'31';") || !strings.Contains(text, hex.EncodeToString([]byte("O'Reilly\\n中文"))) || strings.Contains(text, "O'Reilly") {
		t.Fatalf("unsafe or wrong identity SQL copy: %s %v", text, err)
	}
	doc.page.Rows[0][1].Null = true
	text, err = mysqlDataSQLSelection(doc, false)
	if err != nil || !strings.Contains(text, "NULL") {
		t.Fatalf("SQL NULL lost: %s %v", text, err)
	}
	doc.page.Rows[0][1].Truncated = true
	if _, err = mysqlDataSQLSelection(doc, false); err == nil {
		t.Fatal("truncated SQL write export was enabled")
	}
	doc.page.Rows[0][1].Truncated = false
	doc.page.RowRefs = nil
	if _, err = mysqlDataSQLSelection(doc, true); err == nil {
		t.Fatal("UPDATE export permitted without unique identity")
	}
}

func TestMySQLDataSQLCopySkipsGeneratedFields(t *testing.T) {
	doc := alignedDataTestTab()
	doc.selectedRow = 0
	doc.grid.Cells.All = true
	doc.page.ColumnMetadata = []mysqlsvc.MySQLStructureColumn{{Name: "id", Extra: "auto_increment"}, {Name: "label", Extra: "STORED GENERATED"}}
	doc.page.Rows[0][1].Truncated = true
	text, err := mysqlDataSQLSelection(doc, false)
	if err != nil || strings.Contains(text, "`label`") || !strings.Contains(text, "`id`") {
		t.Fatalf("generated column copied into INSERT: %s %v", text, err)
	}
}

func TestMySQLDataSQLCopyUsesCompleteDraftInsteadOfTruncatedPreview(t *testing.T) {
	doc := alignedDataTestTab()
	doc.selectedRow = 0
	doc.grid.Cells.All = true
	full := strings.Repeat("中文🙂\n", 600)
	value := mysqlsvc.MySQLDataValue{Text: full}
	doc.page.Rows[0][1] = mysqlDataValuePreview(value)
	key := mysqlDataRowKey(doc, 0)
	doc.edit.Drafts = map[string]*mysqlRowDraft{key: {Key: key, Row: 0, Kind: "update", Values: map[string]mysqlsvc.MySQLDataValue{"label": value}}}
	text, err := mysqlDataSQLSelection(doc, false)
	if err != nil || !strings.Contains(text, hex.EncodeToString([]byte(full))) {
		t.Fatalf("complete edited value was rejected or copied as a preview: %v", err)
	}
	delete(doc.edit.Drafts, key)
	if _, err = mysqlDataSQLSelection(doc, false); err == nil {
		t.Fatal("unread truncated value was offered as executable SQL")
	}
}

func TestMySQLDataSQLMenuCopiesCompleteRecordsFromSingleCell(t *testing.T) {
	_, doc, tt, _ := editingTestWorkspace(t)
	doc.hiddenColumns["extra"] = true
	tt.Frame()
	if err := tt.Click("alpha"); err != nil {
		t.Fatal(err)
	}
	tt.Key(0, ui.KeyEnter)
	if err := tt.RightClick("alpha"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("复制为", "Insert 语句"); err != nil {
		t.Fatal(err)
	}
	if got := tt.Clipboard(); !strings.Contains(got, "(`id`, `note`, `extra`) VALUES") || !strings.Contains(got, hex.EncodeToString([]byte("same-row"))) || !strings.Contains(got, "X'3431'") {
		t.Fatalf("single-cell INSERT did not copy the complete existing record and its key: %s", got)
	}
	if err := tt.RightClick("alpha"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("复制为", "Update 语句"); err != nil {
		t.Fatal(err)
	}
	if got := tt.Clipboard(); !strings.Contains(got, "SET `note` =") || !strings.Contains(got, "`extra` =") || strings.Contains(got, "SET `id` =") || !strings.Contains(got, "WHERE CAST(`id` AS BINARY) = X'3431'") {
		t.Fatalf("single-cell UPDATE omitted hidden writable fields or changed its identity: %s", got)
	}
	doc.page.Rows[0][2].Truncated = true
	if _, err := mysqlDataSQLSelection(doc, false); err == nil {
		t.Fatal("complete-record SQL copied an unread hidden-field preview")
	}
}

func TestMySQLDataJSONCopyMatchesObservedIndentation(t *testing.T) {
	got := mysqlDataJSONPreview([]string{"note"}, [][]string{{"中文"}}, [][]bool{{false}})
	want := "[\n  {\n    \"note\": \"中文\"\n  }\n]"
	if got != want {
		t.Fatalf("JSON layout differs from observed single-cell output: %q", got)
	}
}

func TestMySQLDataPasteShortcutReturnsToGridAndStaysInsideEditor(t *testing.T) {
	_, doc, tt, f := editingTestWorkspace(t)
	doc.edit.Batch = true
	if err := tt.Click("alpha"); err != nil {
		t.Fatal(err)
	}
	tt.Key(0, ui.KeyEnter)
	tt.SetClipboard("中文🙂\t右侧")
	tt.Key(ui.Ctrl, ui.KeyV)
	tt.Frame()
	if doc.edit.Editor != nil || doc.page.Rows[0][1].Text != "中文🙂" || doc.page.Rows[0][2].Text != "右侧" || doc.page.Rows[1][1].Text != "beta" || f.writeCount() != 0 {
		t.Fatal("paste shortcut after Enter did not stage the focused grid rectangle")
	}
	if err := tt.Click("beta"); err != nil {
		t.Fatal(err)
	}
	tt.SetClipboard("编辑内粘贴")
	tt.Key(ui.Ctrl, ui.KeyV)
	tt.Frame()
	if doc.edit.Editor == nil || doc.edit.Editor.Text != "编辑内粘贴" || mysqlDataDraftForRow(doc, 1) != nil || doc.page.Rows[1][1].Text != "beta" || doc.page.Rows[0][2].Text != "右侧" || f.writeCount() != 0 {
		t.Fatal("text-editor paste leaked into grid record actions")
	}
	tt.Key(0, ui.KeyEscape)
}

func TestMySQLDataPendingRowMarkersDoNotChangeGridRowHeight(t *testing.T) {
	for _, zoom := range []int{100, 200} {
		t.Run(fmt.Sprint(zoom), func(t *testing.T) {
			s, doc, tt, _ := editingTestWorkspace(t)
			doc.edit.Batch, doc.layout.showRowNumbers = true, true
			doc.edit.Zoom = zoom
			doc.offset = 1234567
			tt.SetSize(840, 560)
			tt.Frame()
			first, ok := tt.Find("alpha")
			if !ok {
				t.Fatal("first fixture row missing")
			}
			second, ok := tt.Find("beta")
			if !ok {
				t.Fatal("second fixture row missing")
			}
			before := second.Y - first.Y
			s.setMySQLDataCell(ui.Services{}, doc, 0, 1, mysqlsvc.MySQLDataValue{Text: "changed-with-marker"})
			tt.Frame()
			first, ok = tt.Find("changed-with-marker")
			if !ok {
				t.Fatal("pending fixture row missing")
			}
			second, _ = tt.Find("beta")
			marker, ok := tt.Find("* 1234568")
			if !ok || second.Y-first.Y != before || marker.H > before {
				t.Fatalf("pending marker changed native row height: before=%v after=%v marker=%+v", before, second.Y-first.Y, marker)
			}
		})
	}
}

func TestMySQLDataUUIDVariantsUseVersionFour(t *testing.T) {
	for _, upper := range []bool{false, true} {
		for _, compact := range []bool{false, true} {
			value, err := mysqlDataUUID(upper, compact)
			if err != nil {
				t.Fatal(err)
			}
			if upper && value != strings.ToUpper(value) || !upper && value != strings.ToLower(value) {
				t.Fatalf("wrong UUID casing %q", value)
			}
			if compact && len(value) != 32 || !compact && len(value) != 36 {
				t.Fatalf("wrong UUID layout %q", value)
			}
			raw, err := hex.DecodeString(strings.ReplaceAll(value, "-", ""))
			if err != nil || raw[6]>>4 != 4 || raw[8]>>6 != 2 {
				t.Fatalf("invalid UUID v4 %q", value)
			}
		}
	}
}

func TestMySQLDataPasteColumnsFollowVisibleOrder(t *testing.T) {
	doc := alignedDataTestTab()
	doc.grid.Columns.Order = []string{"label", "row_header", "id"}
	if got := mysqlDataPasteColumns(doc); len(got) != 2 || got[0] != 1 || got[1] != 0 {
		t.Fatalf("paste ignored reordered grid %v", got)
	}
	doc.hiddenColumns = map[string]bool{"id": true}
	if got := mysqlDataPasteColumns(doc); len(got) != 1 || got[0] != 1 {
		t.Fatalf("hidden column entered paste %v", got)
	}
}

func TestMySQLRecordMenuKeepsTextActionsScopedAndZoomFunctional(t *testing.T) {
	s := New(Dependencies{})
	doc := alignedDataTestTab()
	doc.selectedRow = 0
	doc.grid.Cells.All = true
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c)
		ui.Button(c, "记录菜单").Menu(func(m *ui.Menu) { s.mysqlDataCellMenu(c, doc, m, 0, 1) })
	}, 500, 500)
	if err := tt.Click("记录菜单"); err != nil {
		t.Fatal(err)
	}
	menu := tt.Menu()
	want := []string{"设置为空字符串", "设置为 NULL", "生成 UUID", "在单元格编辑器中编辑", "添加记录", "删除记录", "复制", "复制字段名称", "复制为", "粘贴", "保存数据为"}
	previous := -1
	for _, name := range want {
		found := -1
		for i, label := range menu {
			if strings.Contains(label, name) {
				found = i
				break
			}
		}
		if found <= previous {
			t.Fatalf("menu order or action absent for %q: %v", name, menu)
		}
		previous = found
	}
	for _, name := range []string{"剪切", "全选", "撤销"} {
		for _, label := range menu {
			if strings.Contains(label, name) {
				t.Fatalf("text edit action leaked to record menu: %v", menu)
			}
		}
	}
	if err := tt.ChooseMenuItem("缩放", "150%"); err != nil {
		t.Fatal(err)
	}
	if doc.edit.Zoom != 150 {
		t.Fatal("zoom action had no state effect")
	}
	if err := tt.Click("记录菜单"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("设置为 NULL"); err == nil {
		t.Fatal("read-only sample mutation menu enabled")
	}
}

func TestMySQLDataUUIDAndEmptyActionsRespectFieldTypes(t *testing.T) {
	size := int64(32)
	col := mysqlsvc.MySQLStructureColumn{DataType: "varchar", CharacterMaxLength: &size}
	if mysqlDataUUIDAvailable(col, false) || !mysqlDataUUIDAvailable(col, true) {
		t.Fatal("UUID length did not respect field capacity")
	}
	for _, kind := range []string{"enum", "set", "json", "bigint", "decimal", "date", "binary", "blob", "geometry"} {
		if mysqlDataUUIDAvailable(mysqlsvc.MySQLStructureColumn{DataType: kind}, false) {
			t.Fatalf("text UUID accepted for %s", kind)
		}
	}
	for _, kind := range []string{"bigint", "decimal", "json", "enum", "bit", "date", "geometry"} {
		if mysqlDataEmptyValueAvailable(mysqlsvc.MySQLStructureColumn{DataType: kind}) {
			t.Fatalf("empty string accepted for %s", kind)
		}
	}
	empty := mysqlDataEmptyValue(mysqlsvc.MySQLStructureColumn{DataType: "blob"})
	if !empty.Binary || empty.Bytes == nil || len(empty.Bytes) != 0 || empty.Null {
		t.Fatal("empty binary became NULL or text")
	}
}

func TestMySQLDataSQLCopyPreservesDraftDefaults(t *testing.T) {
	doc := alignedDataTestTab()
	doc.page.Database = "db"
	doc.page.Table = "records"
	doc.selectedRow = 0
	doc.grid.Cells.All = true
	doc.page.Rows[0][0].Text = "(Default)"
	doc.page.Rows[0][1].Text = "(Default)"
	doc.edit.Drafts = map[string]*mysqlRowDraft{"new": {Key: "new", Row: 0, Kind: "insert", Values: map[string]mysqlsvc.MySQLDataValue{"label": {Text: "(Default)"}}}}
	text, err := mysqlDataSQLSelection(doc, false)
	if err != nil || !strings.Contains(text, "VALUES (DEFAULT, _utf8mb4 X'") || strings.Count(text, "DEFAULT") != 1 {
		t.Fatalf("default and literal spelling confused: %q %v", text, err)
	}
	doc.edit.Drafts["new"].Values["label"] = mysqlsvc.MySQLDataValue{Default: true}
	text, err = mysqlDataSQLSelection(doc, false)
	if err != nil || !strings.Contains(text, "VALUES (DEFAULT, DEFAULT)") {
		t.Fatalf("explicit defaults lost: %q %v", text, err)
	}
}

func TestMySQLBatchPendingBarDiscardsTheSameDrafts(t *testing.T) {
	s := New(Dependencies{})
	doc := alignedDataTestTab()
	doc.edit.Batch = true
	doc.page.Rows[0][1].Text = "modified"
	doc.edit.Drafts = map[string]*mysqlRowDraft{"row": {Key: "row", Row: 0, Kind: "update", Before: []mysqlsvc.MySQLDataCell{{Text: "1"}, {Text: "before"}}, Values: map[string]mysqlsvc.MySQLDataValue{"label": {Text: "modified"}}}}
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.mysqlDataFooter(c, doc, doc.page) }, 400, 200)
	if !tt.HasText("1 条待处理记录") {
		t.Fatalf("pending record count missing: %v", tt.Texts())
	}
	if err := tt.Click("放弃待处理记录"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if mysqlDataHasDraft(doc) || doc.page.Rows[0][1].Text != "before" || tt.HasText("1 条待处理记录") {
		t.Fatal("pending bar did not use normal draft cancellation")
	}
}

func TestMySQLDataInvalidBinaryEditorCancelLeavesNoEmptyDraft(t *testing.T) {
	_, doc, tt, f := editingTestWorkspace(t)
	doc.edit.Editor = &mysqlCellEditor{Key: mysqlDataRowKey(doc, 0), Field: "note", Row: 0,
		Text: "61", Initial: "61", Original: mysqlsvc.MySQLDataValue{Binary: true, Bytes: []byte{'a'}},
		Dedicated: true, Open: true}
	tt.Frame()
	if err := tt.Click("专用单元格编辑 note"); err != nil {
		t.Fatal(err)
	}
	tt.Command("selectAll")
	tt.Type("invalid hex")
	if err := tt.Click("应用单元格编辑"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if doc.edit.Editor == nil || doc.edit.Err == "" || len(doc.edit.Drafts) != 0 {
		t.Fatal("invalid binary input did not preserve its editor without staging a record")
	}
	if err := tt.Click("取消单元格编辑"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if mysqlDataHasDraft(doc) || doc.edit.Editor != nil || doc.page.Rows[0][1].Text != "alpha" || f.writeCount() != 0 {
		t.Fatal("canceling invalid binary input left an empty draft or changed the original data")
	}
}
