package objectworkspace

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

// Native RT declarations use the same owned schema document and receipts.
// New tables and existing single ADD/DROP changes share native field types.
func (s *State) manticoreSchemaFields(c *ui.Context, d *Document, locked bool) {
	x := d.schemaDesign
	if x.original.Schema.Exists {
		ui.Text(c, locales.Text("每次添加或删除一个 RT 字段；原有字段名称、类型和表选项保持不变。"))
	} else {
		ui.Text(c, locales.Text("创建指定名称的 RT 表；表名和字段名使用小写，id 固定为原生 bigint。添加所需字段后预览完整建表 SQL。"))
	}
	ui.Row(c).Gap(8).Children(func() {
		if ui.Button(c, locales.Text("字段")).Clicked() {
			x.page = 0
		}
		if ui.Button(c, locales.Text("原始 DDL")).Clicked() {
			x.page = 5
		}
		if x.owner != nil && ui.Button(c, locales.Text("原始结构回执")).Clicked() {
			x.page = 6
		}
	})
	if x.page == 5 {
		raw := x.original.Schema.DDL
		ui.TextArea(c, &raw).Label(locales.Text("原始 DDL")).ReadOnly(true).Grow(1)
		return
	}
	if x.page == 6 && x.owner != nil {
		s.schemaReceiptView(c, x.owner)
		return
	}
	x.page = 0
	if ui.Button(c, locales.Text("添加字段")).Disabled(locked || x.original.Schema.Exists && x.dirty()).Clicked() {
		x.nextID++
		x.draft.Clauses = append(x.draft.Clauses, schemaddl.Clause{ID: fmt.Sprintf("new:%d", x.nextID), Kind: "column", SQL: "integer"})
	}
	remove := -1
	ui.Scroll(c).Grow(1).MinHeight(0).Children(func() {
		ui.Column(c).Gap(8).Children(func() {
			for i := range x.draft.Clauses {
				column := &x.draft.Clauses[i]
				fresh := strings.HasPrefix(column.ID, "new:")
				ui.Row(c.Key(column.ID)).Gap(8).AlignItems(ui.Center).Children(func() {
					theme.Field(c, locales.Text("字段名称"), func() {
						ui.TextInput(c, &column.Name).Label(fmt.Sprintf(locales.Text("RT 字段名称 %d"), i+1)).ReadOnly(locked || !fresh).FillWidth()
					}).Grow(1)
					theme.Field(c, locales.Text("字段类型"), func() {
						if fresh {
							ui.Select(c, &column.SQL, []string{"integer", "bigint", "float", "bool", "timestamp", "string", "multi", "multi64", "json", "text"}).Label(fmt.Sprintf(locales.Text("RT 字段类型 %d"), i+1)).Disabled(locked).FillWidth()
						} else {
							ui.Text(c, column.SQL)
						}
					}).Width(210)
					if ui.Button(c, locales.Text("删除 RT 字段")).Label(fmt.Sprintf(locales.Text("删除 RT 字段 %s"), column.Name)).Disabled(locked || column.Name == "id" || !fresh && x.dirty()).Clicked() {
						remove = i
					}
				})
			}
		})
	})
	if remove >= 0 {
		x.draft.Clauses = append(x.draft.Clauses[:remove], x.draft.Clauses[remove+1:]...)
	}
}
