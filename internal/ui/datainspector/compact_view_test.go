package datainspector

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

// A toolbar that overflows puts its actions in a menu. All three inspector
// actions must be directly reachable in a narrow panel in either locale.
func TestInspectorChunkActionsFitNarrowPanelInBothLocales(t *testing.T) {
	defer i18n.SetLocaleFunc(nil)
	for _, lang := range []string{"zh-Hans", "en"} {
		t.Run(lang, func(t *testing.T) {
			i18n.SetLocaleFunc(func() string { return lang })
			doc := sampleSelection()
			s, p := testInspector(doc)
			doc.Row = 0
			doc.Column = "label"
			doc.Page.RowRefs = []*mysqlsvc.MySQLRowRef{{Database: "test_schema", Table: "entries"}}
			p.chunkField, p.chunkRow = "label", 0
			p.chunkNext, p.chunkTotal = 1024, 8192
			p.chunkText = "chunk data"
			p.chunkPage = doc.Page

			for _, width := range []int{205, 180} {
				tt := ui.NewTester(func(c *ui.Context) {
					theme.Apply(c)
					s.FieldView(c, p)
				}, width, 400)

				for _, source := range []string{"复制单元格预览", "读取所选字段下一段", "复制此片段"} {
					label := locales.TextIn(lang, source)
					r, ok := tt.Find(label)
					if !ok || r.W <= 0 || r.H <= 0 || r.X < 0 || r.X+r.W > float32(width) ||
						r.Y < 42 || r.Y+r.H > 42+theme.CommandBarHeight {
						t.Fatalf("%s: action %q overflowed inspector width %d: %+v (found=%t)",
							lang, label, width, r, ok)
					}
				}
				if err := tt.Click(locales.TextIn(lang, "复制此片段")); err != nil {
					t.Fatal(err)
				}
				if tt.Clipboard() != p.chunkText {
					t.Fatalf("%s: copy chunk action no longer works", lang)
				}
			}
		})
	}
}

func TestMySQLInspectorQualifiedFieldHeaderFitsNarrowWidth(t *testing.T) {
	doc := sampleSelection()
	s, p := testInspector(doc)
	for _, width := range []int{180, 205} {
		tt := ui.NewTester(func(c *ui.Context) {
			theme.Apply(c)
			s.FieldView(c, p)
		}, width, 480)
		for _, label := range []string{"id", "test_schema.entries", "bigint unsigned", "auto_increment"} {
			r, ok := tt.Find(label)
			if !ok || r.X < 0 || r.X+r.W > float32(width) {
				t.Fatalf("metadata %q clipped beyond %dpx inspector: %+v found=%v", label, width, r, ok)
			}
		}
	}
}

func TestInspectorLargeFieldsKeepCopyAndChunkActionsVisible(t *testing.T) {
	doc := sampleSelection()
	s, p := testInspector(doc)
	doc.Row = 0
	doc.Column = "label"
	doc.Page.RowRefs = []*mysqlsvc.MySQLRowRef{{Database: "test_schema", Table: "entries"}}
	doc.Page.Rows[0][1].Text = strings.Repeat("large preview ", 300)
	doc.Page.ColumnMetadata[1].Comment = strings.Repeat("long metadata ", 200)
	p.chunkField, p.chunkRow = "label", 0
	p.chunkNext, p.chunkTotal = 1024, 8192
	p.chunkText = "0x" + strings.Repeat("ab", 1024)
	p.chunkPage = doc.Page
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c)
		s.FieldView(c, p)
	}, 205, 400)
	header, ok := tt.Find("数据信息切换栏")
	if !ok || header.Y != 0 || header.H != 42 {
		t.Fatalf("inspector header no longer occupies the fixed top region: %+v", header)
	}
	actionsTop := header.Y + header.H
	actionsBottom := actionsTop + theme.CommandBarHeight
	for _, label := range []string{"复制单元格预览", "读取所选字段下一段", "复制此片段"} {
		r, ok := tt.Find(label)
		if !ok || r.X < 0 || r.X+r.W > 205 || r.Y < actionsTop || r.Y+r.H > actionsBottom {
			t.Fatalf("large values displaced inspector action %s: %+v", label, r)
		}
	}
	if err := tt.Click("复制此片段"); err != nil {
		t.Fatal(err)
	}
	if tt.Clipboard() != p.chunkText {
		t.Fatal("bounded display altered the copied fragment")
	}
}
