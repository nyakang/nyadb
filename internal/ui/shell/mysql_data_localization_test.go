package shell

import (
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func useEnglishDataWorkspace(t *testing.T) {
	t.Helper()
	i18n.SetLocaleFunc(func() string { return "en" })
	t.Cleanup(func() { i18n.SetLocaleFunc(nil) })
}

func TestMySQLDataStatusEnglishKeepsPageAndQueryMeaning(t *testing.T) {
	useEnglishDataWorkspace(t)
	doc := &tab{dataLimit: 100, dataOffset: 100}
	for _, test := range []struct {
		loading, canceled bool
		err               string
		page              *connections.MySQLDataPage
		want              string
	}{
		{want: "No data loaded"},
		{loading: true, want: "Loading page 2…"},
		{canceled: true, want: "Read canceled · Page 2"},
		{err: "permission denied", want: "Read failed · Page 2"},
		{page: &connections.MySQLDataPage{}, want: "Loaded 0 rows · Page 2"},
		{page: &connections.MySQLDataPage{Rows: [][]connections.MySQLDataCell{{{Text: "value"}}}}, want: "Loaded 1 rows · 101–101 · Page 2"},
	} {
		doc.dataLoading, doc.dataReadInfo.Canceled, doc.dataErr, doc.dataPage = test.loading, test.canceled, test.err, test.page
		if got := mysqlDataPageStatus(doc); got != test.want {
			t.Fatalf("status = %q, want %q", got, test.want)
		}
	}
	doc.dataReadInfo.QuerySQL = "SELECT `字段信息` FROM `表信息` LIMIT ?"
	doc.dataLoading = true
	if got := mysqlDataQueryStatus(doc); got != "Last successful query (parameterized): "+doc.dataReadInfo.QuerySQL {
		t.Fatalf("translated SQL or incorrect read state: %q", got)
	}
	doc.dataReadInfo.CompletedAt = time.Date(2026, 10, 26, 12, 30, 0, 0, time.Local)
	if got := mysqlDataReadStatus(doc); !strings.HasPrefix(got, "Last successful read 12:30:00") {
		t.Fatalf("read time not translated: %q", got)
	}
}

func TestMySQLFieldInspectorTranslatesLabelsAndPreservesValues(t *testing.T) {
	useEnglishDataWorkspace(t)
	doc := mysqlInspectorSampleTab()
	doc.dataSelectedRow = 0
	doc.dataState.Cells = ui.ColumnRange{Cursor: "label", Anchor: "label"}
	doc.dataPage.Rows[0][1].Text = "复制"
	doc.dataPage.ColumnMetadata[1].Comment = "主键"
	literalDefault := "否"
	doc.dataPage.ColumnMetadata[1].Default = &literalDefault
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c)
		(&State{}).mysqlDataInspector(c, doc)
	}, 330, 800)
	for _, label := range []string{"Data field information panel", "Field info", "Not NULL", "Default value", "Character set", "Complete preview"} {
		if !tt.HasText(label) {
			t.Fatalf("missing English field label %q: %v", label, tt.Texts())
		}
	}
	for _, value := range []string{"复制", "主键", "\"否\"", "test_schema.entries"} {
		if !tt.HasText(value) {
			t.Fatalf("server value was translated: %q", value)
		}
	}
	if err := tt.Click("Copy Cell Preview"); err != nil {
		t.Fatal(err)
	}
	if tt.Clipboard() != "复制" {
		t.Fatalf("copied translated cell content: %q", tt.Clipboard())
	}
}

func TestMySQLTableInspectorTranslatesLabelsAndPreservesServerError(t *testing.T) {
	useEnglishDataWorkspace(t)
	doc := tableInfoSampleTab()
	doc.dataInfo.attempted = true
	doc.dataInfo.err = "表信息"
	doc.dataInfo.info = &connections.MySQLTableInfo{Database: doc.dataDatabase, Name: doc.dataTable, Kind: "BASE TABLE", Engine: "InnoDB", Comment: "刷新"}
	s := tableInfoSampleState(doc)
	tt := tableInfoTester(s, doc)
	for _, label := range []string{"Data table information panel", "Table info", "Estimated rows", "Get row count", "Row counts ignore the current filters.", "Unable to load table information: 表信息"} {
		if !tt.HasText(label) {
			t.Fatalf("missing English table label or changed error %q: %v", label, tt.Texts())
		}
	}
}
