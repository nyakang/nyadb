package shell

import (
	"context"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/ui/dataworkspace"
)

func newDataDocument(s *State, id int64, db, table string) *dataworkspace.Document {
	s.nextTabID++
	return s.dataWorkspace.Open(s.nextTabID, id, db, table)
}
func fixtureDataTab(t *testing.T, s *State, page *mysqlsvc.MySQLDataPage) *tab {
	t.Helper()
	s.sessions.Current().ProfileID = 19
	s.sessions.Current().Status = "已连接"
	s.dataWorkspace.Loader = func(context.Context, int64, string, string, int64, int) (*mysqlsvc.MySQLDataPage, error) {
		return page, nil
	}
	s.openMySQLData(ui.Services{}, "edit_test", "records")
	d := s.currentTab()
	deadline := time.Now().Add(time.Second)
	for d.data.Loading() && time.Now().Before(deadline) {
		s.dataWorkspace.Consume(ui.Services{})
		time.Sleep(time.Millisecond)
	}
	if d.data.Loading() {
		t.Fatal("fixture did not load")
	}
	return d
}
func editableHostPage() *mysqlsvc.MySQLDataPage {
	ref := &mysqlsvc.MySQLRowRef{Database: "edit_test", Table: "records", Columns: []string{"id"}, Values: [][]byte{[]byte("41")}}
	hash := "fingerprint"
	return &mysqlsvc.MySQLDataPage{Database: "edit_test", Table: "records", Limit: 100, StableOrder: true, Columns: []string{"id", "note"}, ColumnKinds: []string{"bigint", "varchar"}, ColumnMetadata: []mysqlsvc.MySQLStructureColumn{{Name: "id", DataType: "bigint", Key: "PRI"}, {Name: "note", DataType: "varchar", Nullable: true}}, Rows: [][]mysqlsvc.MySQLDataCell{{{Text: "41"}, {Text: "alpha"}}}, RowRefs: []*mysqlsvc.MySQLRowRef{ref}, RowSnapshots: []*mysqlsvc.MySQLRowSnapshot{{Ref: *ref, Columns: []string{"id", "note"}, Fingerprints: []*string{&hash, &hash}}}}
}
func setTestInput(t *testing.T, tt *ui.Tester, label, value string) {
	t.Helper()
	if err := tt.Click(label); err != nil {
		t.Fatal(err)
	}
	tt.Command("selectAll")
	tt.Type(value)
	tt.Frame()
}

func TestPreviewInteractionsDoNotBorrowLiveModuleState(t *testing.T) {
	s := New()
	s.navigation.Key = "live selection"
	s.sessions.Current().Database = "live_db"
	s.objectWorkspace.SetFilter(23, "视图")
	s.objectWorkspace.SelectRow(23, 4)
	tt := ui.NewTester(s.View, 1280, 800)
	if err := tt.Click("视图"); err != nil {
		t.Fatal(err)
	}
	setTestInput(t, tt, "搜索连接", "测试")
	s.openData("customers")
	tt.Frame()
	if s.navigation.Key != "live selection" || s.sessions.Current().Database != "live_db" || s.objectWorkspace.FilterKind(23) != "视图" || s.objectWorkspace.SelectedRow(23) != 4 {
		t.Fatal("preview changed live navigation, session or object selection")
	}
	if s.preview.kind != "视图" || s.preview.search != "测试" {
		t.Fatal("preview did not own its interaction state")
	}
}
