package structureviewer

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func TestStructureTabsAndEmptyHeadersSurviveLoadFailure(t *testing.T) {
	s := New(Dependencies{})
	doc := s.Open(1, 1, "example", "records")
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c)
		s.View(c, doc)
	}, 384, 400)
	before, ok := tt.Find("索引")
	if !ok || !tt.HasText("不是 null") {
		t.Fatal("unloaded structure lost tabs or column headers")
	}
	doc.err = strings.Repeat("metadata unavailable ", 40)
	tt.Frame()
	after, ok := tt.Find("索引")
	if !ok || before.Y != after.Y {
		t.Fatalf("status message moved structure tabs: before=%+v after=%+v", before, after)
	}
	r, ok := tt.Find("重试读取结构")
	if !ok || r.X+r.W > 384 {
		t.Fatalf("long error pushed retry offscreen: %+v", r)
	}
	doc.data = &mysqlsvc.MySQLTableStructure{}
	doc.err = ""
	if err := tt.Click("外键"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("被引用的数据库") || !tt.HasText("此表没有外键") {
		t.Fatal("empty foreign-key page lost its schema or empty state")
	}
}

func TestStructurePagesKeepReadOnlyMetadataAtWorkbenchSizes(t *testing.T) {
	for _, locale := range []string{"zh-Hans", "en"} {
		t.Run(locale, func(t *testing.T) {
			i18n.SetLocaleFunc(func() string { return locale })
			t.Cleanup(func() { i18n.SetLocaleFunc(nil) })
			s, _ := testViewer()
			doc := s.Open(1, 1, "真实库", "订单")
			doc.data = fakeStructure("真实库", "订单")
			doc.data.Columns = append(doc.data.Columns, mysqlsvc.MySQLStructureColumn{Name: "备注", Type: "varchar(64)", DataType: "varchar", Comment: "客户备注"})
			doc.data.Indexes = append(doc.data.Indexes, mysqlsvc.MySQLStructureIndex{Name: "idx_note", Type: "BTREE", Columns: []mysqlsvc.MySQLStructureIndexColumn{{Name: "备注", Position: 1}}})
			back := 0
			s.deps.Back = func() { back++ }
			tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.View(c, doc) }, 1280, 800)
			for _, size := range [][2]int{{1280, 800}, {840, 560}} {
				tt.SetSize(size[0], size[1])
				for _, label := range []string{"字段", "索引", "外键", "表信息", "返回对象列表"} {
					rect, ok := tt.Find(locales.Text(label))
					if !ok || rect.X < 0 || rect.Y < 0 || rect.X+rect.W > float32(size[0]) || rect.Y+rect.H > float32(size[1]) {
						t.Fatalf("%s is outside viewport %v: %+v", label, size, rect)
					}
				}
				if err := tt.Click(locales.Text("字段")); err != nil {
					t.Fatal(err)
				}
				if !tt.HasText("备注") || !tt.HasText("客户备注") {
					t.Fatal("field metadata changed or disappeared")
				}
				if err := tt.Click("备注"); err != nil {
					t.Fatal(err)
				}
				if field, ok := doc.SelectedField(); !ok || field.Name != "备注" {
					t.Fatal("selected field was not exposed to host inspector")
				}
				if err := tt.Click(locales.Text("索引")); err != nil {
					t.Fatal(err)
				}
				if !tt.HasText("idx_note") || tt.HasText("PRIMARY") {
					t.Fatal("secondary index grouping changed")
				}
				if err := tt.Click(locales.Text("外键")); err != nil {
					t.Fatal(err)
				}
				if !tt.HasText("fk_order") || !tt.HasText("accounts") {
					t.Fatal("foreign-key identity changed")
				}
				if err := tt.Click(locales.Text("表信息")); err != nil {
					t.Fatal(err)
				}
				if !tt.HasText("真实库") || !tt.HasText("订单") || !tt.HasText("InnoDB") {
					t.Fatal("raw database/table metadata was translated or lost")
				}
			}
			if err := tt.Click(locales.Text("返回对象列表")); err != nil {
				t.Fatal(err)
			}
			if back != 1 || len(doc.Data().Indexes) != 2 || doc.Data().Indexes[0].Name != "PRIMARY" {
				t.Fatal("back callback or complete service metadata changed")
			}
			s.CloseAll()
		})
	}
}
