package shell

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func TestMySQLDataBlockCopyUsesNativeSelectionAndKeepsMenuBlock(t *testing.T) {
	s := New()
	doc := alignedDataTestTab()
	doc.dataSelectedRow = -1
	doc.dataPage.Rows = [][]connections.MySQLDataCell{
		{{Text: "41"}, {Text: "alpha"}}, {{Text: "42"}, {Text: "beta"}}, {{Text: "43"}, {Text: "gamma"}},
	}
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.mysqlDataWorkspace(c, doc) }, 900, 500)
	if err := tt.Click("41"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ClickWith(ui.Shift, "beta"); err != nil {
		t.Fatal(err)
	}
	tt.Key(ui.Ctrl, ui.KeyC)
	if got := tt.Clipboard(); got != "41\talpha\n42\tbeta" {
		t.Fatalf("native block copy: %q", got)
	}
	if err := tt.RightClick("alpha"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("复制"); err != nil {
		t.Fatal(err)
	}
	if tt.Clipboard() != "41\talpha\n42\tbeta" || doc.dataSelection.Len() != 2 {
		t.Fatal("right-click inside the block discarded the selection")
	}
	if err := tt.RightClick("gamma"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("复制"); err != nil {
		t.Fatal(err)
	}
	if tt.Clipboard() != "gamma" || doc.dataSelectedRow != 2 {
		t.Fatal("right-click outside the block did not target the clicked cell")
	}
}

func TestMySQLCopyFormatsPreserveHiddenReorderedNullAndQuotedValues(t *testing.T) {
	s := New()
	doc := alignedDataTestTab()
	doc.dataPage.Columns = []string{"id", "label", "hidden"}
	doc.dataColumns = append(doc.dataColumns, ui.TableColumn{ID: "hidden", Title: "hidden", Width: 100})
	doc.dataPage.Rows = [][]connections.MySQLDataCell{
		{{Text: "9007199254740993"}, {Text: "a,\"b\"\tline\nnext"}, {Text: "private"}},
		{{Text: "2"}, {Null: true}, {Text: "private"}},
		{{Text: "3"}, {Text: ""}, {Text: "private"}},
	}
	doc.dataHiddenColumns = map[string]bool{"hidden": true}
	doc.dataState.Columns.Order = []string{"hidden", "label", "id"}
	doc.dataState.Cells = ui.ColumnRange{All: true, Cursor: "label", Anchor: "label"}
	doc.dataSelection.Add(2)
	doc.dataSelection.Add(0)
	doc.dataSelection.Add(1)
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.mysqlDataWorkspace(c, doc) }, 900, 500)
	for _, format := range []struct {
		label string
		comma rune
	}{{"逗号分隔值（字段名称和数据）", ','}, {"制表符分隔值（字段名称和数据）", '\t'}} {
		if err := tt.Click("复制为"); err != nil {
			t.Fatal(err)
		}
		if err := tt.ChooseMenuItem(format.label); err != nil {
			t.Fatal(err)
		}
		r := csv.NewReader(strings.NewReader(tt.Clipboard()))
		r.Comma = format.comma
		got, err := r.ReadAll()
		want := [][]string{{"label", "id"}, {"a,\"b\"\tline\nnext", "9007199254740993"}, {"NULL", "2"}, {"", "3"}}
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: %v %v", format.label, got, err)
		}
	}
	if err := tt.Click("复制为"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("JSON"); err != nil {
		t.Fatal(err)
	}
	var got []map[string]any
	if err := json.Unmarshal([]byte(tt.Clipboard()), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || len(got[0]) != 2 || got[0]["id"] != "9007199254740993" || got[1]["label"] != nil || got[2]["label"] != "" {
		t.Fatalf("JSON lost null/empty/precision/hidden-field boundaries: %s", tt.Clipboard())
	}
	doc.dataPage.Rows[0][1].Truncated = true
	tt.Frame()
	if err := tt.Click("复制所选数据预览"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tt.Clipboard(), "预览已截断") || strings.Contains(tt.Clipboard(), "private") {
		t.Fatal("copy lost the truncation notice or included hidden data")
	}
}

func TestMySQLDataCopyShortcutStaysInGridAndRefreshUsesService(t *testing.T) {
	s := New()
	doc := alignedDataTestTab()
	doc.dataProfileID, doc.dataDatabase = 7, "fixture"
	s.mysqlSessionProfileID, s.mysqlSessionStatus = doc.dataProfileID, "已连接"
	doc.dataRulesExpanded = true
	calls := make(chan connections.MySQLDataOptions, 2)
	s.mysqlDataAdvancedLoader = func(_ context.Context, _ int64, _, _ string, o connections.MySQLDataOptions) (*connections.MySQLDataPage, error) {
		calls <- o
		return &connections.MySQLDataPage{Columns: []string{"id", "label"}}, nil
	}
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.mysqlDataWorkspace(c, doc) }, 900, 600)
	if err := tt.Click("数据筛选值"); err != nil {
		t.Fatal(err)
	}
	tt.Type("draft value")
	tt.Key(ui.Ctrl, ui.KeyA)
	tt.Key(ui.Ctrl, ui.KeyC)
	tt.Key(0, ui.KeyF5)
	if tt.Clipboard() != "draft value" || len(calls) != 0 || doc.dataLoading {
		t.Fatal("grid shortcuts intercepted the filter input")
	}
	if err := tt.Click("safe-value"); err != nil {
		t.Fatal(err)
	}
	tt.Key(ui.Ctrl, ui.KeyA)
	tt.Key(ui.Ctrl, ui.KeyC)
	if tt.Clipboard() != "1\tsafe-value" {
		t.Fatalf("select all copied something other than loaded visible data: %q", tt.Clipboard())
	}
	tt.Key(0, ui.KeyF5)
	select {
	case o := <-calls:
		if o.Offset != 0 || o.After != nil || !o.UseKeyset {
			t.Fatalf("refresh did not request the first page: %+v", o)
		}
	case <-time.After(time.Second):
		t.Fatal("grid refresh did not invoke the data service")
	}
	s.cancelMySQLData(doc)
	if doc.dataSelectedRow != -1 || doc.dataSelection.Len() != 0 || doc.dataPage != nil {
		t.Fatal("refresh retained a stale selection")
	}
}
