package dataworkspace

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func TestHeaderHoverAndSelectionNeverCallAdvancedLoader(t *testing.T) {
	s, doc, tt, fixture := editingTestWorkspace(t)
	requests := make(chan mysqlsvc.MySQLDataOptions, 4)
	s.AdvancedLoader = func(_ context.Context, _ int64, _, _ string, options mysqlsvc.MySQLDataOptions) (*mysqlsvc.MySQLDataPage, error) {
		requests <- options
		return cloneEditingPage(fixture.page), nil
	}
	for i := range doc.columns {
		doc.columns[i].Sortable = true
	}
	doc.page.ColumnMetadata[1].Comment = "cached field comment"
	tt.Frame()
	if err := tt.Click("id"); err != nil {
		t.Fatal(err)
	}
	if doc.loading || len(requests) != 0 {
		t.Fatal("header selection called the data loader")
	}
	header, ok := tt.Find("note")
	if !ok {
		t.Fatal("missing comment-bearing header")
	}
	tt.Move(header.X+header.W/2, header.Y+header.H/2)
	tt.Advance(2 * time.Second)
	if !tt.HasText("cached field comment") {
		t.Fatal("cached COMMENT did not appear after the hover delay")
	}
	if doc.loading || len(requests) != 0 {
		t.Fatal("header hover fetched data instead of using cached metadata")
	}
	if err := tt.ClickWith(ui.Cmd, "extra"); err != nil {
		t.Fatal(err)
	}
	tt.Advance(time.Second)
	if doc.loading || len(requests) != 0 || doc.sort.Column != "" || !reflect.DeepEqual(mysqlDataSelectedColumns(doc), []string{"id", "extra"}) {
		t.Fatal("modified header selection launched a query or changed sorting")
	}
}

func TestReorderedDiscreteColumnsProduceOnlySelectedCSVAndJSONExportPayload(t *testing.T) {
	_, doc, tt, _ := editingTestWorkspace(t)
	doc.page.Rows[0][0].Text = "9007199254740993"
	doc.page.Rows[0][2].Text = "comma,quote\"\nnext line"
	tt.Frame()
	if err := tt.Click("id"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ClickWith(ui.Cmd, "extra"); err != nil {
		t.Fatal(err)
	}
	doc.grid.Columns.Order = []string{"extra", "note", "id"}
	tt.Frame()
	names, rows, nulls := mysqlDataSelectedPreview(doc)
	if !reflect.DeepEqual(names, []string{"extra", "id"}) {
		t.Fatalf("reordering changed selected identities or included the middle field: %v", names)
	}
	// These are the same serializers and options exportMySQLDataSelection
	// uses before its asynchronous file chooser/write.
	csvPayload := mysqlDataDelimitedPreview(names, rows, ',', true, true)
	parsed, err := csv.NewReader(strings.NewReader(csvPayload)).ReadAll()
	wantCSV := [][]string{{"extra", "id"}, {"comma,quote\"\nnext line", "9007199254740993"}, {"NULL", "42"}, {"", "43"}}
	if err != nil || !reflect.DeepEqual(parsed, wantCSV) {
		t.Fatalf("CSV export payload lost selection, quoting, nulls or precision: %v %v", parsed, err)
	}
	jsonPayload := mysqlDataJSONPreview(names, rows, nulls)
	var parsedJSON []map[string]any
	if err := json.Unmarshal([]byte(jsonPayload), &parsedJSON); err != nil {
		t.Fatal(err)
	}
	wantJSON := []map[string]any{{"extra": "comma,quote\"\nnext line", "id": "9007199254740993"}, {"extra": nil, "id": "42"}, {"extra": "", "id": "43"}}
	if !reflect.DeepEqual(parsedJSON, wantJSON) || strings.Index(jsonPayload, `"extra":`) > strings.Index(jsonPayload, `"id":`) {
		t.Fatalf("JSON export payload lost displayed order or value identity: %s", jsonPayload)
	}
	for _, format := range []struct{ label, payload string }{{"逗号分隔值（字段名称和数据）", csvPayload}, {"JSON", jsonPayload}} {
		if err := tt.RightClick("extra"); err != nil {
			t.Fatal(err)
		}
		if err := tt.ChooseMenuItem("复制为", format.label); err != nil {
			t.Fatal(err)
		}
		if tt.Clipboard() != format.payload {
			t.Fatalf("native copy-as and export serializer disagreed for %s: %q", format.label, tt.Clipboard())
		}
	}
}

func TestPasteFromDiscreteColumnCursorUsesContinuousVisibleColumns(t *testing.T) {
	_, doc, tt, fixture := editingTestWorkspace(t)
	doc.edit.Batch = true
	doc.grid.Columns.Order = []string{"note", "id", "extra"}
	tt.Frame()
	if err := tt.Click("extra"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ClickWith(ui.Cmd, "note"); err != nil {
		t.Fatal(err)
	}
	if doc.grid.Cells.Cursor != "note" || !reflect.DeepEqual(mysqlDataSelectedColumns(doc), []string{"note", "extra"}) {
		t.Fatalf("test did not establish a discrete header selection: %v cursor=%s", mysqlDataSelectedColumns(doc), doc.grid.Cells.Cursor)
	}
	tt.SetClipboard("changed-note\t9007199254740993")
	tt.Key(ui.Cmd, ui.KeyV)
	tt.Frame()
	if doc.page.Rows[0][1].Text != "changed-note" || doc.page.Rows[0][0].Text != "9007199254740993" || doc.page.Rows[0][2].Text != "same-row" || doc.page.Rows[1][1].Text != "beta" || doc.edit.Editor != nil || fixture.writeCount() != 0 {
		t.Fatalf("paste followed discrete selected IDs or affected other rows: row=%+v editor=%+v writes=%d", doc.page.Rows[0], doc.edit.Editor, fixture.writeCount())
	}
	draft := mysqlDataDraftForRow(doc, 0)
	if draft == nil || len(draft.Values) != 2 || draft.Values["note"].Text != "changed-note" || draft.Values["id"].Text != "9007199254740993" {
		t.Fatal("continuous paste did not stage exactly its two visible destination fields")
	}
}

func TestDataHeadersSelectDiscreteColumnsWithoutQueryAndCopyActualSelection(t *testing.T) {
	_, doc, tt, _ := editingTestWorkspace(t)
	for i := range doc.columns {
		doc.columns[i].Sortable = true
	}
	tt.Frame()
	if err := tt.Click("id"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ClickWith(ui.Cmd, "extra"); err != nil {
		t.Fatal(err)
	}
	if doc.loading || doc.sort.Column != "" || doc.edit.Editor != nil || doc.selection.Len() != 3 || !reflect.DeepEqual(mysqlDataSelectedColumns(doc), []string{"id", "extra"}) {
		t.Fatalf("headers did not select only the requested columns: cols=%v rows=%d sort=%+v loading=%v", mysqlDataSelectedColumns(doc), doc.selection.Len(), doc.sort, doc.loading)
	}
	tt.Key(ui.Cmd, ui.KeyC)
	if want := "41\tsame-row\n42\tNULL\n43\t"; tt.Clipboard() != want {
		t.Fatalf("discrete selection copied intermediate fields: %q", tt.Clipboard())
	}
	if err := tt.RightClick("extra"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("复制字段名称"); err != nil {
		t.Fatal(err)
	}
	if tt.Clipboard() != "id\textra" {
		t.Fatalf("selected header menu lost discrete selection: %q", tt.Clipboard())
	}
	if err := tt.RightClick("note"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("复制"); err != nil {
		t.Fatal(err)
	}
	if tt.Clipboard() != "alpha\nbeta\ngamma" || !reflect.DeepEqual(mysqlDataSelectedColumns(doc), []string{"note"}) {
		t.Fatalf("unselected header did not target its column: %q %v", tt.Clipboard(), mysqlDataSelectedColumns(doc))
	}
}

func TestDataHeaderActionAddsFocusedDraftWithoutQuery(t *testing.T) {
	_, doc, tt, _ := editingTestWorkspace(t)
	header, ok := tt.Find("note")
	if !ok {
		t.Fatal("missing field header")
	}
	tt.Move(header.X+header.W/2, header.Y+header.H/2)
	if err := tt.Click("note actions"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("添加筛选"); err != nil {
		t.Fatal(err)
	}
	if !doc.rulesExpanded || doc.filterColumnDraft != "note" || doc.filterModeDraft != "精确匹配" || doc.filterValueDraft != "" || doc.loading || doc.filterMode != "" || len(doc.filterRules) != 0 {
		t.Fatalf("header filter was applied instead of drafted: %+v", doc)
	}
	tt.Type("draft-only")
	if doc.filterValueDraft != "draft-only" || doc.loading {
		t.Fatal("header filter value was not focused or typing queried the server")
	}
}

func TestDataHeaderActionKeepsSelectionUntilExplicitRemoteSort(t *testing.T) {
	s, doc, tt, fixture := editingTestWorkspace(t)
	for i := range doc.columns {
		doc.columns[i].Sortable = true
	}
	doc.filterColumn, doc.filterMode, doc.filterValue = "note", "equal", "alpha"
	requests := make(chan mysqlsvc.MySQLDataOptions, 2)
	s.AdvancedLoader = func(_ context.Context, _ int64, _, _ string, options mysqlsvc.MySQLDataOptions) (*mysqlsvc.MySQLDataPage, error) {
		requests <- options
		return cloneEditingPage(fixture.page), nil
	}
	tt.Frame()
	if err := tt.Click("id"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ClickWith(ui.Cmd, "extra"); err != nil {
		t.Fatal(err)
	}
	header, _ := tt.Find("note")
	tt.Move(header.X+header.W/2, header.Y+header.H/2)
	if err := tt.Click("note actions"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(mysqlDataSelectedColumns(doc), []string{"id", "extra"}) || len(requests) != 0 {
		t.Fatal("opening the action button selected or queried its field")
	}
	if err := tt.ChooseMenuItem("降序排序"); err != nil {
		t.Fatal(err)
	}
	waitMySQLData(t, s, doc, tt)
	options := <-requests
	if options.SortColumn != "note" || !options.SortDescending || options.FilterColumn != "note" || options.FilterMode != "equal" || options.FilterValue != "alpha" || options.Offset != 0 || options.After != nil || doc.grid.ColumnSelection != nil {
		t.Fatalf("header sort changed filters, replayed the page or retained stale selection: %+v", options)
	}
}

func TestDataHeaderColumnSelectionExcludesStatisticsAndKeepsRecordCursor(t *testing.T) {
	s, doc, _, _ := editingTestWorkspace(t)
	s.openAnalysis(doc)
	doc.tools.analysisScope = "page"
	s.startDataAnalysis(ui.Services{}, doc)
	doc.selectedRow = 1
	tt := ui.NewTester(func(c *ui.Context) { s.Consume(c.Services()); theme.Apply(c); s.View(c, doc) }, 1400, 750)
	if err := tt.Click("note"); err != nil {
		t.Fatal(err)
	}
	if doc.selection.Len() != 3 || doc.selection.Has(-1) || doc.selectedRow != 1 {
		t.Fatalf("whole column included statistics or moved the record cursor: rows=%d row=%d", doc.selection.Len(), doc.selectedRow)
	}
	tt.Key(ui.Cmd, ui.KeyC)
	if tt.Clipboard() != "alpha\nbeta\ngamma" {
		t.Fatalf("column copy included a statistical/display row: %q", tt.Clipboard())
	}
	tt.Key(0, ui.KeyF2)
	if doc.edit.Editor == nil || doc.edit.Editor.Row != 1 || doc.edit.Editor.Text != "beta" {
		t.Fatal("editing after column selection used a display-row identity")
	}
}

func TestDataHeaderFilterPreservesExistingDraftAndRejectsSeventhCondition(t *testing.T) {
	s := New(Dependencies{})
	doc := alignedDataTestTab()
	doc.filterColumnDraft, doc.filterModeDraft, doc.filterValueDraft = "id", "大于", "7"
	doc.filterExtraDraft = []mysqlDataRuleDraft{{Column: "label", Mode: "类似", Value: "a%"}}
	s.mysqlDataAddHeaderFilter(doc, "label")
	if doc.filterValueDraft != "7" || doc.filterExtraDraft[0].Value != "a%" || len(doc.filterExtraDraft) != 1 || doc.layout.filterFocus != "label" {
		t.Fatal("existing filter draft was replaced")
	}
	for len(doc.filterExtraDraft) < mysqlsvc.MySQLDataMaxFilterRules-1 {
		doc.filterExtraDraft = append(doc.filterExtraDraft, mysqlDataRuleDraft{Column: "other", Mode: "精确匹配"})
	}
	s.mysqlDataAddHeaderFilter(doc, "new")
	if len(doc.filterExtraDraft) != mysqlsvc.MySQLDataMaxFilterRules-1 || doc.ruleError == "" || doc.loading {
		t.Fatal("seventh header filter was accepted or queried")
	}
}

func TestEmptyDataHeaderKeepsMenusWidthAndFieldNameCopy(t *testing.T) {
	s := New(Dependencies{})
	doc := layoutTestTab()
	doc.page.Rows = nil
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.View(c, doc) }, 700, 450)
	if err := tt.RightClick("label"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("复制字段名称"); err != nil {
		t.Fatal(err)
	}
	if tt.Clipboard() != "label" || doc.selectedRow != -1 {
		t.Fatalf("empty header created a row or could not copy its identity: %q row=%d", tt.Clipboard(), doc.selectedRow)
	}
	if err := tt.RightClick("label"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("复制"); err == nil {
		t.Fatal("empty-page data copy was enabled")
	}
	tt.Key(0, ui.KeyEscape)
	if err := tt.RightClick("label"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("列宽…"); err != nil {
		t.Fatal(err)
	}
	if !doc.layout.columnWidth.Open || doc.layout.columnWidth.Column != "label" || doc.layout.columnWidth.Value != "160" {
		t.Fatal("empty header did not open the width editor with its current value")
	}
	if err := tt.Click("列宽（DIP）"); err != nil {
		t.Fatal(err)
	}
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Type("245")
	if err := tt.Click("确定"); err != nil {
		t.Fatal(err)
	}
	if doc.grid.Columns.Widths["label"] != 245 || doc.layout.columnWidth.Open {
		t.Fatal("empty-table column width dialog did not update its native layout")
	}
}

func TestDataPlaceholderHeaderRevokesPageCallbacksAndRestoresOnSuccess(t *testing.T) {
	s := New(Dependencies{})
	doc := layoutTestTab()
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.View(c, doc) }, 700, 450)
	page := doc.page
	doc.page = nil
	tt.Frame()
	if doc.grid.HeaderSelect || doc.grid.HeaderMenu != nil || doc.grid.HeaderActionMenu != nil || doc.grid.OnCellClick != nil || doc.grid.CellMenu != nil || doc.grid.Sort != nil {
		t.Fatal("placeholder headers retained callbacks to the previous page")
	}
	if err := tt.RightClick("label"); err != nil {
		t.Fatal(err)
	}
	if len(tt.Menu()) != 0 {
		t.Fatal("placeholder header exposed a stale page menu")
	}
	doc.page = page
	tt.Frame()
	if !doc.grid.HeaderSelect || doc.grid.HeaderMenu == nil || doc.grid.HeaderActionMenu == nil || doc.grid.OnCellClick == nil {
		t.Fatal("successful data page did not restore native header actions")
	}
}

func TestDataColumnWidthRequiresIntegerWithinBoundsAndPreservesCanceledValue(t *testing.T) {
	doc := layoutTestTab()
	doc.columns[1].MinWidth, doc.columns[1].MaxWidth = 65, 300
	doc.grid.Columns.Widths = map[string]float32{"label": 180}
	mysqlDataOpenColumnWidth(doc, 1)
	for _, value := range []string{"", "0", "-1", "64", "301", "100.5", "NaN", "999999999999999999999999999999999999999999"} {
		doc.layout.columnWidth.Value = value
		if mysqlDataApplyColumnWidth(doc) || doc.grid.Columns.Widths["label"] != 180 || doc.layout.columnWidth.Err == "" {
			t.Fatalf("invalid width %q changed the layout", value)
		}
	}
	doc.layout.columnWidth.Value = "245"
	if !mysqlDataApplyColumnWidth(doc) || doc.grid.Columns.Widths["label"] != 245 || doc.layout.columnWidth.Open {
		t.Fatal("valid width was not committed")
	}
	mysqlDataOpenColumnWidth(doc, 1)
	doc.layout.columnWidth = mysqlDataColumnWidthState{}
	if doc.grid.Columns.Widths["label"] != 245 {
		t.Fatal("cancel changed the column width")
	}
}

func TestDataHeaderCommentsStayIndependentAndTooltipOnlyContainsComment(t *testing.T) {
	s := New(Dependencies{})
	doc := layoutTestTab()
	doc.page.ColumnMetadata = []mysqlsvc.MySQLStructureColumn{{Name: "id", Type: "bigint"}, {Name: "label", Type: "varchar(80)", Comment: "first line\nsecond line"}}
	_, columns := mysqlVisibleDataColumns(doc, doc.page)
	if columns[0].HeaderTooltip != "" || columns[1].HeaderTooltip != "first line\nsecond line" || columns[1].HeaderDetail != "" {
		t.Fatal("hover comments included extra metadata or enabled the display row")
	}
	doc.layout.showComments, doc.showFieldTypes = true, false
	_, columns = mysqlVisibleDataColumns(doc, doc.page)
	if columns[0].HeaderDetail != "--" || columns[1].HeaderDetail != "first line\nsecond line" || columns[1].Subtitle != "" {
		t.Fatal("comments depended on type display or lost empty placeholders")
	}
	doc.layout.showComments = false
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.View(c, doc) }, 700, 450)
	header, _ := tt.Find("label")
	tt.Move(header.X+header.W/2, header.Y+header.H/2)
	tt.Advance(2 * time.Second)
	if !tt.HasText("first line\nsecond line") {
		t.Fatal("comment tooltip did not appear over the native header")
	}
}

func TestDataLayoutRestoresCommentAndZoomWithoutPersistingCommentOrSelection(t *testing.T) {
	s := New(Dependencies{})
	saved := map[string]string{}
	memoryLayoutPreferences(s, saved)
	doc := layoutTestTab()
	s.restoreMySQLDataLayout(doc)
	doc.layout.showComments, doc.edit.Zoom = true, 150
	doc.grid.ColumnSelection = []string{"label"}
	doc.page.ColumnMetadata = []mysqlsvc.MySQLStructureColumn{{Name: "id"}, {Name: "label", Comment: "private-comment"}}
	s.Close(doc)
	value := saved[mysqlDataLayoutKey(doc)]
	if !strings.Contains(value, `"show_comments":true`) || !strings.Contains(value, `"zoom":150`) || strings.Contains(value, "private-comment") || strings.Contains(value, "ColumnSelection") {
		t.Fatalf("layout preference lost settings or included data: %s", value)
	}
	reopened := layoutTestTab()
	s.restoreMySQLDataLayout(reopened)
	if !reopened.layout.showComments || mysqlDataZoom(reopened) != 150 {
		t.Fatal("new presentation settings did not survive close")
	}
	for _, value := range []string{`{"version":1,"showFieldTypes":true}`, `{"version":1,"zoom":123,"showFieldTypes":true}`} {
		legacy := layoutTestTab()
		saved[mysqlDataLayoutKey(legacy)] = value
		s.restoreMySQLDataLayout(legacy)
		if legacy.layout.showComments || mysqlDataZoom(legacy) != 100 {
			t.Fatalf("legacy or unsupported zoom changed defaults: %s", value)
		}
	}
}

func TestDiscreteColumnsDriveFillHideAndDockWithoutIntermediateFields(t *testing.T) {
	s, doc, tt, _ := editingTestWorkspace(t)
	doc.grid.ColumnSelection = []string{"extra", "id"}
	doc.grid.Cells = ui.ColumnRange{Anchor: "id", Cursor: "extra"}
	doc.grid.Columns.Order = []string{"extra", "note", "id"}
	for row := range doc.page.Rows {
		doc.selection.Add(row)
	}
	doc.selectedRow = 0
	doc.fill.Mode, doc.fill.Text = "值", "filled"
	if err := s.fillSelectedData(doc); err != nil {
		t.Fatal(err)
	}
	for row := range doc.page.Rows {
		if doc.page.Rows[row][1].Text == "filled" || doc.page.Rows[row][2].Text != "filled" {
			t.Fatal("fill touched an intermediate unselected field")
		}
	}
	doc.toolbar.dock = "文本"
	tt.Frame()
	if doc.edit.Editor != nil || len(mysqlDataSelectedColumns(doc)) != 2 {
		t.Fatal("dock collapsed discrete column selection")
	}
	mysqlDataHideColumn(doc, doc.page, "extra")
	if !reflect.DeepEqual(mysqlDataSelectedColumns(doc), []string{"id"}) || doc.grid.Cells.Cursor != "id" {
		t.Fatalf("hiding a selected field lost the remaining selection: %v cursor=%s", mysqlDataSelectedColumns(doc), doc.grid.Cells.Cursor)
	}
}

func TestColumnFitMeasuresCachedTextCommentsAndZoom(t *testing.T) {
	doc := layoutTestTab()
	doc.page.Rows[0][1].Text = strings.Repeat("W", 80)
	doc.page.ColumnMetadata = []mysqlsvc.MySQLStructureColumn{{Name: "id"}, {Name: "label", Type: "varchar(300)", Comment: strings.Repeat("annotation ", 50)}}
	var c1, c2, comments float32
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c)
		mysqlDataFitColumn(c, doc, doc.page, 1)
		c1 = doc.grid.Columns.Widths["label"]
		doc.edit.Zoom = 200
		mysqlDataFitColumn(c, doc, doc.page, 1)
		c2 = doc.grid.Columns.Widths["label"]
		doc.layout.showComments = true
		mysqlDataFitColumn(c, doc, doc.page, 1)
		comments = doc.grid.Columns.Widths["label"]
	}, 700, 450)
	_ = tt
	if c1 <= 420 || c2 < c1*1.9 || comments <= c2 {
		t.Fatalf("fit ignored full cached text, zoom or comments: %v %v %v", c1, c2, comments)
	}
}
