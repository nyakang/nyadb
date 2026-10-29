package shell

import (
	"context"
	"testing"

	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

func TestMySQLCompactHeaderAt1280AndNarrowNavigation(t *testing.T) {
	s, tt := structureTestBrowser(t)
	s.dataWorkspace.AdvancedLoader = func(ctx context.Context, id int64, db, table string, o mysqlsvc.MySQLDataOptions) (*mysqlsvc.MySQLDataPage, error) {
		return &mysqlsvc.MySQLDataPage{
			Database: db, Table: table, Limit: o.Limit, Offset: o.Offset, StableOrder: true,
			Columns: []string{"id", "name"}, ColumnKinds: []string{"bigint", "varchar"},
			ColumnMetadata: []mysqlsvc.MySQLStructureColumn{
				{Name: "id", DataType: "bigint", Type: "bigint unsigned", Key: "PRI"},
				{Name: "name", DataType: "varchar", Type: "varchar(100)"},
			},
			Rows: [][]mysqlsvc.MySQLDataCell{{{Text: "1"}, {Text: "sample"}}},
		}, nil
	}
	if err := tt.Click("打开选中表数据"); err != nil {
		t.Fatal(err)
	}
	waitMySQLData(t, s, tt)
	header, ok := tt.Find("bigint unsigned")
	if !ok {
		t.Fatalf("double row title missing: %v", tt.Texts())
	}
	t.Logf("1280x800 native subtitle bounds: %+v", header)
	if header.Y > 190 {
		t.Fatalf("compact grid header too low at 1280: %+v", header)
	}
	if !tt.HasText("筛选 0 · 排序 0") || !tt.HasText("刷新表数据：orders") {
		t.Fatalf("compact navigation or count missing: %v", tt.Texts())
	}
	if err := tt.Click("筛选 & 排序"); err != nil {
		t.Fatal(err)
	}
	if _, found := tt.Find("筛选规则"); !found {
		t.Fatal("full rule editor inaccessible")
	}
	if err := tt.Click("筛选 & 排序"); err != nil {
		t.Fatal(err)
	}
	if _, found := tt.Find("筛选规则"); found {
		t.Fatal("could not collapse")
	}

	tt.SetSize(840, 560)
	small := tt
	small.Frame()
	inspector, ok := small.Find("数据表信息面板")
	if !ok {
		t.Fatal("inspector missing in narrow window")
	}
	for _, name := range []string{"筛选 & 排序", "对象", "刷新表数据：orders", "清除筛选"} {
		r, ok := small.Find(name)
		t.Logf("840 control %s: %+v found=%v", name, r, ok)
		if !ok || r.X+r.W > inspector.X {
			t.Fatalf("%s overflows into inspector: %+v inspector=%+v", name, r, inspector)
		}
	}
	if !small.HasText("对象") || !small.HasText("刷新表数据：orders") {
		t.Fatal("narrow window lost navigation or refresh")
	}
	if err := small.Click("对象"); err != nil {
		t.Fatal(err)
	}
	if s.currentTab().kind != tabObjects {
		t.Fatal("Objects tab did not return to the object browser")
	}
}
