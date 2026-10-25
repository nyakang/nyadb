package shell

import (
	"testing"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
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
			doc := mysqlInspectorSampleTab()
			doc.dataSelectedRow = 0
			doc.dataState.Cells = ui.ColumnRange{Cursor: "label", Anchor: "label"}
			doc.dataPage.RowRefs = []*connections.MySQLRowRef{{Database: "test_schema", Table: "entries"}}
			doc.dataChunkField, doc.dataChunkRow = "label", 0
			doc.dataChunkNext, doc.dataChunkTotal = 1024, 8192
			doc.dataChunkText = "chunk data"

			for _, width := range []int{205, 180} {
				tt := ui.NewTester(func(c *ui.Context) {
					theme.Apply(c)
					(&State{}).mysqlDataInspector(c, doc)
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
				if tt.Clipboard() != doc.dataChunkText {
					t.Fatalf("%s: copy chunk action no longer works", lang)
				}
			}
		})
	}
}
