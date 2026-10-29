package shell

import (
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
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
		page              *mysqlsvc.MySQLDataPage
		want              string
	}{
		{want: "No data loaded"},
		{loading: true, want: "Loading page 2…"},
		{canceled: true, want: "Read canceled · Page 2"},
		{err: "permission denied", want: "Read failed · Page 2"},
		{page: &mysqlsvc.MySQLDataPage{}, want: "Loaded 0 rows · Page 2"},
		{page: &mysqlsvc.MySQLDataPage{Rows: [][]mysqlsvc.MySQLDataCell{{{Text: "value"}}}}, want: "Loaded 1 rows · 101–101 · Page 2"},
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

func TestMySQLRuleChoicesTranslateWithoutChangingQueryIDs(t *testing.T) {
	useEnglishDataWorkspace(t)
	doc := alignedDataTestTab()
	doc.dataRulesExpanded = true
	doc.dataFilterColumnDraft, doc.dataFilterModeDraft = "label", "包含"
	s := New()
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c)
		s.mysqlDataWorkspace(c, doc)
	}, 840, 560)
	if !tt.HasText("Contains") || !tt.HasText("label") {
		t.Fatal("rule labels not localized")
	}
	if err := tt.Click("Contains"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("Is NULL"); err != nil {
		t.Fatal(err)
	}
	if doc.dataFilterModeDraft != "为空" || mysqlFilterMode(doc.dataFilterModeDraft) != "is_null" || doc.dataFilterColumnDraft != "label" {
		t.Fatal("localized choice changed the stored query identifiers")
	}
}
