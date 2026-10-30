package dataworkspace

import (
	"context"
	"fmt"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func TestDataAnalysisUsesCurrentPageAndExactNumericOrdering(t *testing.T) {
	d := &Document{database: "sample", table: "numbers", offset: 100, page: &mysqlsvc.MySQLDataPage{
		Columns: []string{"amount", "label"}, ColumnKinds: []string{"decimal", "varchar"},
		Rows: [][]mysqlsvc.MySQLDataCell{
			{{Text: "9007199254740993"}, {Text: "repeat"}},
			{{Text: "9007199254740992"}, {Text: "repeat"}},
			{{Null: true}, {Text: ""}},
			{{Text: "900719925474099", Truncated: true}, {Text: "partial", Truncated: true}},
			{{Text: "bad"}, {Null: true}},
			{},
		},
	}}
	a := mysqlDataAnalyzePage(d)
	if a == nil || a.Offset != 100 || a.Rows != 6 || !strings.Contains(mysqlDataAnalysisScope(a), "101–106") {
		t.Fatal("analysis lost the current loaded-page range")
	}
	number, text := a.Columns[0], a.Columns[1]
	if number.Numeric != 2 || number.Null != 1 || number.Incomplete != 2 || number.NumericInvalid != 1 || number.Unique != 4 || number.Singletons != 4 ||
		number.Min != "9007199254740992" || number.Max != "9007199254740993" || number.Mean != "9007199254740992.50000000" {
		t.Fatalf("numeric statistics rounded integers or included truncated/missing values: %+v", number)
	}
	if text.Unique != 3 || text.Singletons != 2 || text.Null != 1 || text.Incomplete != 2 || text.Frequencies[0].Value != "repeat" || text.Frequencies[0].Count != 2 || text.Frequencies[1].Value != "" {
		t.Fatalf("NULL, empty strings or complete-value distribution were combined: %+v", text)
	}
	d.page.Rows[0][0].Text = "mutated"
	if a.Columns[0].Max != "9007199254740993" {
		t.Fatal("analysis snapshot retained mutable page values")
	}
}

func TestDataAnalysisRejectsUnboundedNumericInputAndHandlesEmptyPage(t *testing.T) {
	for _, text := range []string{"NaN", "Inf", "1e999999999", "1e-999999999", "1/2", " 3", strings.Repeat("1", 257)} {
		if _, ok := mysqlDataAnalysisNumber(text); ok {
			t.Fatalf("invalid or unbounded numeric input accepted: %s", text)
		}
	}
	if value, ok := mysqlDataAnalysisNumber("1.25e2"); !ok || value.Cmp(big.NewRat(125, 1)) != 0 {
		t.Fatal("finite MySQL exponent notation rejected")
	}
	d := &Document{page: &mysqlsvc.MySQLDataPage{Columns: []string{"empty"}}}
	a := mysqlDataAnalyzePage(d)
	if a == nil || a.Rows != 0 || a.Columns[0].Unique != 0 || !strings.Contains(mysqlDataAnalysisScope(a), "为空") {
		t.Fatal("confirmed empty page invented a row range or nonempty aggregate")
	}
	d.loading = true
	if mysqlDataAnalyzePage(d) != nil {
		t.Fatal("pending page displayed statistics as current")
	}
}

func TestDataAnalysisInlineSettingsResultsAndExit(t *testing.T) {
	s := New(Dependencies{})
	d := layoutTestTab()
	d.offset = 50
	s.openAnalysis(d)
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.View(c, d); s.toolsModals(c, d) }, 1100, 650)
	if !tt.HasText("开始分析") || !tt.HasText("safe-value") || d.tools.analysis != nil || d.tools.analysisScope != "all" {
		t.Fatalf("analysis did not enter inline settings over the existing table: %v", tt.Texts())
	}
	d.tools.analysisScope = "page"
	tt.Frame()
	if err := tt.Click("开始分析"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText(mysqlDataAnalysisScope(d.tools.analysis)) || !tt.HasText("1 非重复, 1 唯一") || !tt.HasText("safe-value") || d.tools.analysisSettings {
		t.Fatalf("inline results lost data, scope, or header statistics: %v", tt.Texts())
	}
	if err := tt.Click("列统计"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if d.toolbar.dock != "列统计" || !tt.HasText("值分布（最多 10 项）") {
		t.Fatal("column statistics did not dock")
	}
	if err := tt.Click("退出分析"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if d.tools.analysisOpen || d.tools.analysis != nil || d.toolbar.dock != "" || !tt.HasText("safe-value") {
		t.Fatal("exit retained analysis or lost the original table")
	}
}

func analysisAsyncTestDocument() (*State, *Document, *Session) {
	session := &Session{ProfileID: 7, Generation: 2, Connected: true}
	s := New(Dependencies{Session: func() Session { return *session }})
	d := s.Open(1, 7, "sample", "entries")
	fixture := layoutTestTab()
	d.page, d.columns = fixture.page, fixture.columns
	s.openAnalysis(d)
	d.tools.analysisScope = "page"
	s.startDataAnalysis(ui.Services{}, d)
	return s, d, session
}

func TestDataAnalysisFullRequestOwnsFiltersAndParksForOriginalProfile(t *testing.T) {
	s, d, session := analysisAsyncTestDocument()
	d.filterRules = []mysqlsvc.MySQLDataFilterRule{{Column: "label", Mode: "equal", Value: "captured"}}
	d.tools.analysisScope = "filtered"
	started, release := make(chan struct{}), make(chan struct{})
	s.Analyzer = func(_ context.Context, profile int64, db, table string, options mysqlsvc.MySQLDataOptions, fields []string) (*mysqlsvc.MySQLDataAnalysis, error) {
		close(started)
		<-release
		if profile != 7 || db != "sample" || table != "entries" || options.Filters[0].Value != "captured" || fields[0] != "id" || options.Limit != 0 || options.Offset != 0 {
			t.Error("full analysis shared mutable settings or applied page limits")
		}
		return &mysqlsvc.MySQLDataAnalysis{Rows: 999, Columns: []mysqlsvc.MySQLDataColumnAnalysis{{Name: "label", Rows: 999, Null: 4, Distinct: 12, Singletons: 3, Collation: "utf8mb4_0900_ai_ci"}}}, nil
	}
	s.startDataAnalysis(ui.Services{}, d)
	<-started
	d.filterRules[0].Value, d.page.Columns[0] = "mutated", "changed"
	session.ProfileID = 8
	close(release)
	result := <-d.tools.analysisResults
	out := make(chan mysqlDataAnalysisResult, 1)
	out <- result
	d.tools.analysisResults = out
	s.consumeDataAnalysis()
	if !d.tools.analysisBusy || d.tools.analysis.Scope != "page" || len(d.tools.analysisResults) != 1 {
		t.Fatal("parked analysis was consumed while another profile was active")
	}
	session.ProfileID = 7
	s.consumeDataAnalysis()
	if d.tools.analysisBusy || d.tools.analysis.Rows != 999 || d.tools.analysis.Scope != "filtered" || d.tools.analysis.FilterCount != 1 || d.tools.analysis.Columns[0].Singletons != 3 {
		t.Fatal("original profile did not receive its qualified complete aggregate")
	}
}

func TestDataAnalysisCancellationWaitsForAcknowledgementAndGenerationMismatchKeepsCache(t *testing.T) {
	s, d, session := analysisAsyncTestDocument()
	d.tools.analysisScope = "all"
	started, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	s.Analyzer = func(ctx context.Context, _ int64, _, _ string, _ mysqlsvc.MySQLDataOptions, _ []string) (*mysqlsvc.MySQLDataAnalysis, error) {
		close(started)
		<-ctx.Done()
		close(canceled)
		<-release
		return nil, ctx.Err()
	}
	previous := d.tools.analysis
	s.startDataAnalysis(ui.Services{}, d)
	<-started
	s.cancelDataAnalysis(d)
	<-canceled
	s.consumeDataAnalysis()
	if !d.tools.analysisBusy || !d.tools.analysisCanceling {
		t.Fatal("cancel settled before driver acknowledgement")
	}
	close(release)
	result := <-d.tools.analysisResults
	out := make(chan mysqlDataAnalysisResult, 1)
	out <- result
	d.tools.analysisResults = out
	s.consumeDataAnalysis()
	if d.tools.analysisBusy || d.tools.analysis != previous || d.tools.analysisErr == "" {
		t.Fatal("cancel replaced the previous successful analysis")
	}
	s.Analyzer = func(context.Context, int64, string, string, mysqlsvc.MySQLDataOptions, []string) (*mysqlsvc.MySQLDataAnalysis, error) {
		return &mysqlsvc.MySQLDataAnalysis{Rows: 456}, nil
	}
	s.startDataAnalysis(ui.Services{}, d)
	result = <-d.tools.analysisResults
	out = make(chan mysqlDataAnalysisResult, 1)
	out <- result
	d.tools.analysisResults = out
	session.Generation++
	s.consumeDataAnalysis()
	if d.tools.analysisBusy || d.tools.analysis != previous || d.tools.analysisErr == "" {
		t.Fatal("stale session analysis replaced cached statistics")
	}
}

func TestDataAnalysisCloseRevokesWorkerAndReleasesResults(t *testing.T) {
	s, d, _ := analysisAsyncTestDocument()
	d.tools.analysisScope = "all"
	started, done := make(chan struct{}), make(chan struct{})
	s.Analyzer = func(ctx context.Context, _ int64, _, _ string, _ mysqlsvc.MySQLDataOptions, _ []string) (*mysqlsvc.MySQLDataAnalysis, error) {
		close(started)
		<-ctx.Done()
		close(done)
		return nil, ctx.Err()
	}
	s.startDataAnalysis(ui.Services{}, d)
	<-started
	s.Close(d)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("close did not cancel the aggregate executor")
	}
	if d.tools.analysisResults != nil || d.tools.analysis != nil || d.tools.analysisBusy {
		t.Fatal("closed document retained analysis channels or caches")
	}
}

func TestDataAnalysisHeaderKeepsRecordSelectionCopyAndEditing(t *testing.T) {
	s, d, _, f := editingTestWorkspace(t)
	s.openAnalysis(d)
	d.tools.analysisScope = "page"
	s.startDataAnalysis(ui.Services{}, d)
	tt := ui.NewTester(func(c *ui.Context) { s.Consume(c.Services()); theme.Apply(c); s.View(c, d); s.Modals(c) }, 1400, 750)
	if err := tt.Click("beta"); err != nil {
		t.Fatal(err)
	}
	waitEditing(t, s, d, tt)
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if d.selectedRow != 1 || !d.selection.Has(1) || d.selection.Has(2) {
		t.Fatalf("statistical header shifted pointer selection: %d %v", d.selectedRow, d.selection)
	}
	tt.Key(0, ui.KeyDown)
	tt.Frame()
	if d.selectedRow != 2 || !d.selection.Has(2) {
		t.Fatalf("native keyboard cursor no longer maps to a record: %d %v", d.selectedRow, d.selection)
	}
	tt.Key(ui.Ctrl, ui.KeyC)
	if tt.Clipboard() != "gamma" {
		t.Fatalf("copy used a display row: %q", tt.Clipboard())
	}
	tt.Key(0, ui.KeyHome)
	tt.Frame()
	if d.selectedRow != 0 || d.selection.Has(-1) {
		t.Fatal("Home selected the statistical section")
	}
	tt.Key(0, ui.KeyF2)
	waitEditing(t, s, d, tt)
	if d.edit.Editor == nil || d.edit.Editor.Row != 0 || d.edit.Editor.Field != "note" || d.edit.Editor.Text != "alpha" {
		t.Fatalf("F2 edited a display row: %+v", d.edit.Editor)
	}
	tt.Key(0, ui.KeyEscape)
	d.toolbar.dock = "列统计"
	tt.Frame()
	if err := tt.Click("alpha"); err != nil {
		t.Fatal(err)
	}
	waitEditing(t, s, d, tt)
	if d.edit.Editor == nil || d.edit.Editor.Dedicated {
		t.Fatalf("statistics dock hid the inline cell editor: editor=%+v row=%d range=%+v busy=%v err=%q", d.edit.Editor, d.selectedRow, d.grid.Cells, d.edit.Busy, d.edit.Err)
	}
	s.finishMySQLCellEdit(d, false)
	tt.Frame()
	if err := tt.Click("表头统计"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if d.selectedRow != 0 || !d.selection.Has(0) || d.toolbar.gridOffset != 0 || f.writeCount() != 0 {
		t.Fatal("collapsing statistics changed selection or saved data")
	}
}

func TestDataAnalysisHeaderFollowsReorderedResizedColumnsAndDockSelection(t *testing.T) {
	s, d, _, _ := editingTestWorkspace(t)
	d.grid.Columns.Order = []string{"extra", "note", "id"}
	d.grid.Columns.Widths = map[string]float32{"extra": 205, "note": 190}
	s.openAnalysis(d)
	d.tools.analysisScope = "page"
	s.startDataAnalysis(ui.Services{}, d)
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.View(c, d) }, 1400, 750)
	for _, name := range []string{"extra", "note", "id"} {
		header, ok := tt.Find(name)
		panel, exists := tt.Find("列统计: " + name)
		if !ok || !exists || header.X < panel.X || header.X > panel.X+panel.W {
			t.Fatalf("%s statistics detached from its native column: header=%+v panel=%+v", name, header, panel)
		}
		if width := d.grid.Columns.Widths[name]; width > 0 && panel.W != width {
			t.Fatalf("statistics ignored resized width for %s: %+v", name, panel)
		}
	}
	if err := tt.Click("列统计"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("列统计: extra"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if d.grid.Cells.Cursor != "extra" || !tt.HasText("1 · 33%") || d.edit.Editor != nil {
		t.Fatal("column statistics failed to follow column choice or started editing")
	}
}

func absAnalysisPosition(x float32) float32 {
	if x < 0 {
		return -x
	}
	return x
}

func TestDataAnalysisHeaderSharesHorizontalScrollAndPreservesSelectionAcrossExit(t *testing.T) {
	s, d, _, _ := editingTestWorkspace(t)
	d.grid.Columns.Widths = map[string]float32{"note": 245}
	d.selectedRow = 1
	d.selection.Add(1)
	d.grid.Cells = ui.ColumnRange{Anchor: "note", Cursor: "note"}
	s.openAnalysis(d)
	d.tools.analysisScope = "page"
	s.startDataAnalysis(ui.Services{}, d)
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.View(c, d) }, 460, 650)
	headerBefore, _ := tt.Find("note")
	panelBefore, _ := tt.Find("列统计: note")
	cell, ok := tt.Find("beta")
	if !ok {
		t.Fatal("record missing before horizontal scroll")
	}
	tt.Scroll(cell.X+10, cell.Y+5, 150, 0)
	tt.Frame()
	headerAfter, ok := tt.Find("note")
	panelAfter, exists := tt.Find("列统计: note")
	if !ok || !exists || absAnalysisPosition((headerBefore.X-headerAfter.X)-(panelBefore.X-panelAfter.X)) > 1 || panelBefore.X <= panelAfter.X {
		t.Fatalf("statistics did not share horizontal scroll: header %+v -> %+v, panel %+v -> %+v", headerBefore, headerAfter, panelBefore, panelAfter)
	}
	if d.selectedRow != 1 || !d.selection.Has(1) {
		t.Fatal("scroll changed record identity")
	}
	s.closeDataAnalysis(d)
	tt.Frame()
	if d.selectedRow != 1 || d.selection.Len() != 1 || !d.selection.Has(1) || d.toolbar.gridOffset != 0 || d.grid.Key(1) != 1 {
		t.Fatal("exit changed a selected record key")
	}
}

func TestDataAnalysisNumericMedianDeviationZeroAndNullModes(t *testing.T) {
	d := &Document{page: &mysqlsvc.MySQLDataPage{Columns: []string{"value", "text"}, ColumnKinds: []string{"decimal", "varchar"}, Rows: [][]mysqlsvc.MySQLDataCell{
		{{Text: "25.00"}, {Text: "NULL"}}, {{Null: true}, {Null: true}}, {{Text: "0.00"}, {Text: ""}}, {{Text: "12.50"}, {Null: true}},
	}}}
	a := mysqlDataAnalyzePage(d)
	number, text := a.Columns[0], a.Columns[1]
	if number.Median != "12.50000000" || number.Zero != 1 || mysqlDataAnalysisRounded(number.StdDeviation, 2) != "10.21" || number.ModeCount != 4 || number.MaxFrequency != 1 || len(number.Modes) != 4 {
		t.Fatalf("Navicat fixture statistics mismatch: %+v", number)
	}
	if text.MaxFrequency != 2 || text.ModeCount != 1 || len(text.Modes) != 1 || !text.Modes[0].Null || mysqlDataAnalysisModes(text) != "(NULL)" {
		t.Fatalf("literal NULL or empty string replaced the NULL mode: %+v", text)
	}
	d.page.Rows = [][]mysqlsvc.MySQLDataCell{{{Text: "9007199254740993"}}, {{Text: "9007199254740992"}}}
	exact := mysqlDataAnalyzePage(d).Columns[0]
	if exact.Median != "9007199254740992.50000000" || exact.StdDeviation != "0.50000000" {
		t.Fatalf("median or variance lost differences near 2^53: %+v", exact)
	}
	d.page.Rows = [][]mysqlsvc.MySQLDataCell{{{Null: true}}}
	null := mysqlDataAnalyzePage(d).Columns[0]
	if null.Median != "" || null.StdDeviation != "" || null.Zero != 0 || null.ModeCount != 1 || !null.Modes[0].Null {
		t.Fatal("all-NULL analysis invented numeric statistics")
	}
	d.page.Rows = [][]mysqlsvc.MySQLDataCell{{{Text: "-7"}}}
	single := mysqlDataAnalyzePage(d).Columns[0]
	if single.Median != "-7.00000000" || single.StdDeviation != "0.00000000" {
		t.Fatal("single-value population deviation is not zero")
	}
}

func TestDataAnalysisModesRetainCompleteTieCountWithBoundedLabels(t *testing.T) {
	d := &Document{page: &mysqlsvc.MySQLDataPage{Columns: []string{"value"}, ColumnKinds: []string{"varchar"}}}
	for i := range 25 {
		d.page.Rows = append(d.page.Rows, []mysqlsvc.MySQLDataCell{{Text: fmt.Sprint(i)}})
	}
	stats := mysqlDataAnalyzePage(d).Columns[0]
	if stats.ModeCount != 25 || len(stats.Modes) != 10 || !strings.Contains(mysqlDataAnalysisModes(stats), "另有 15 项") {
		t.Fatalf("bounded mode display silently omitted ties: %+v", stats)
	}
}

func TestDataAnalysisDockDisplaysFullNumericStatistics(t *testing.T) {
	s := New(Dependencies{})
	d := &Document{page: &mysqlsvc.MySQLDataPage{Columns: []string{"amount"}, ColumnKinds: []string{"decimal"}, Rows: [][]mysqlsvc.MySQLDataCell{{{Text: "0.00"}}, {{Text: "12.50"}}, {{Text: "25.00"}}, {{Null: true}}}}, columns: []ui.TableColumn{{ID: "amount", Title: "amount", Width: 155}}}
	s.openAnalysis(d)
	d.tools.analysisScope = "page"
	s.startDataAnalysis(ui.Services{}, d)
	d.toolbar.dock, d.toolbar.dockExpanded = "列统计", true
	d.grid.Cells = ui.ColumnRange{Cursor: "amount", Anchor: "amount"}
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.View(c, d) }, 900, 650)
	for _, text := range []string{"最高出现次数", "零", "中值", "标准差", "10.21", "12.50000000", "0.00, 12.50, 25.00, (NULL)"} {
		if !tt.HasText(text) {
			t.Fatalf("full numeric statistic missing %q: %v", text, tt.Texts())
		}
	}
	value, ok := tt.Find("0.00, 12.50, 25.00, (NULL)")
	if !ok || value.X+value.W > 270 {
		t.Fatalf("mode values escaped their statistics panel: %+v", value)
	}
}

func TestDataAnalysisLargePageVirtualizesRecordsAndRevealsLastRow(t *testing.T) {
	s := New(Dependencies{})
	d := &Document{selectedRow: -1, page: &mysqlsvc.MySQLDataPage{StableOrder: true, Columns: []string{"id", "note"}, ColumnKinds: []string{"bigint", "varchar"}}, columns: []ui.TableColumn{{ID: "id", Title: "id", Width: 120}, {ID: "note", Title: "note", Width: 180}}}
	for i := range 200 {
		d.page.Rows = append(d.page.Rows, []mysqlsvc.MySQLDataCell{{Text: fmt.Sprint(i + 1)}, {Text: fmt.Sprintf("record-%d", i)}})
	}
	s.openAnalysis(d)
	d.tools.analysisScope = "page"
	s.startDataAnalysis(ui.Services{}, d)
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.View(c, d) }, 800, 480)
	if len(d.edit.Hits) >= 100 || !tt.HasText("record-0") || tt.HasText("record-199") {
		t.Fatalf("analysis bypassed native row virtualization: visible_cells=%d", len(d.edit.Hits))
	}
	d.selectedRow = 199
	d.selection.Add(199)
	d.grid.Cells = ui.ColumnRange{Cursor: "note", Anchor: "note"}
	mysqlDataRevealEditRow(d, 199)
	tt.Frame()
	if !tt.HasText("record-199") || len(d.edit.Hits) >= 100 || d.selectedRow != 199 || !d.selection.Has(199) {
		t.Fatalf("statistics header broke last-row reveal or record keys: cells=%d row=%d", len(d.edit.Hits), d.selectedRow)
	}
}
