package datainspector

import (
	"testing"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func useEnglishInspector(t *testing.T) {
	t.Helper()
	i18n.SetLocaleFunc(func() string { return "en" })
	t.Cleanup(func() { i18n.SetLocaleFunc(nil) })
}
func TestMySQLFieldInspectorTranslatesLabelsAndPreservesValues(t *testing.T) {
	useEnglishInspector(t)
	doc := sampleSelection()
	s, p := testInspector(doc)
	doc.Row = 0
	doc.Column = "label"
	doc.Page.Rows[0][1].Text = "复制"
	doc.Page.ColumnMetadata[1].Comment = "主键"
	literalDefault := "否"
	doc.Page.ColumnMetadata[1].Default = &literalDefault
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c)
		s.FieldView(c, p)
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
	useEnglishInspector(t)
	doc := sampleSelection()
	s, p, _ := tableInfoTestState(doc)
	p.info.attempted = true
	p.info.err = "表信息"
	p.info.info = &mysqlsvc.MySQLTableInfo{Database: p.database, Name: p.table, Kind: "BASE TABLE", Engine: "InnoDB", Comment: "刷新"}

	tt := tableInfoTester(s, p)
	for _, label := range []string{"Data table information panel", "Table info", "Estimated rows", "Get row count", "Row counts ignore the current filters.", "Unable to load table information: 表信息"} {
		if !tt.HasText(label) {
			t.Fatalf("missing English table label or changed error %q: %v", label, tt.Texts())
		}
	}
}
