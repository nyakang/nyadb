package objectworkspace

import (
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"

	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

var mysqlStructureColumns = []ui.TableColumn{
	{Title: "名称", Width: 152},
	{Title: "类型", Width: 120},
	{Title: "长度", Width: 78},
	{Title: "小数点", Width: 78},
	{Title: "不是 null", Width: 91},
	{Title: "虚拟", Width: 65},
	{Title: "键", Width: 65},
	{Title: "默认值", Width: 170},
	{Title: "注释", Width: 180},
}
var mysqlStructureIndexColumns = []ui.TableColumn{
	{Title: "名称", Width: 170},
	{Title: "字段", Width: 235},
	{Title: "类型", Width: 100},
	{Title: "方法", Width: 115},
}
var mysqlStructureForeignColumns = []ui.TableColumn{
	{Title: "名称", Width: 115},
	{Title: "字段", Width: 110},
	{Title: "被引用的数据库", Width: 110},
	{Title: "被引用的表", Width: 105},
	{Title: "被引用的字段", Width: 115},
	{Title: "删除时", Width: 95},
	{Title: "更新时", Width: 95},
}

func (s *State) View(c *ui.Context, t *Document) {
	if t == nil || t.closed {
		return
	}
	if t.program != nil {
		t.program.View(c)
		return
	}

	if t == nil || t.closed {
		return
	}
	session := s.session()
	palette := theme.Colors(c)

	ui.Column(c).Fill().Background(palette.App).Children(func() {
		ui.Row(c).Height(theme.RowHeight(c, theme.CommandBarHeight)).Padding(2, 6).AlignItems(ui.Center).Gap(6).
			Background(palette.Header).Children(func() {
			theme.Icon(c, "table", theme.SmallIconSize, palette.Blue)
			ui.Text(c, t.database+" / "+t.table).FontSize(theme.Font(c, theme.SmallFontSize)).SingleLine().Grow(1).MinWidth(0).MaxWidth(220).Tooltip(t.database + " / " + t.table)
			ui.Text(c, locales.Text("设计表 · 只读结构")).TextColor(palette.Muted).FontSize(theme.Font(c, theme.SmallFontSize)).SingleLine().Tooltip(t.database + " / " + t.table)
			ui.Toolbar(c, func() {
				if ui.Button(c, locales.Text("返回")).Label(locales.Text("返回对象列表")).Height(theme.RowHeight(c, theme.ControlHeight)).Clicked() {
					if s.deps.Back != nil {
						s.deps.Back()
					}
				}
				if ui.Button(c, locales.Text("刷新")).Label("刷新表结构：" + t.table).Height(theme.RowHeight(c, theme.ControlHeight)).
					Disabled(!session.Connected || session.ProfileID != t.profileID || t.loading).Clicked() {
					s.Load(t, c.Services().Invalidate, true)
				}
				if t.loading && ui.Button(c, locales.Text("取消加载")).Height(theme.RowHeight(c, theme.ControlHeight)).Clicked() {
					s.Cancel(t)
				}
			}).Label(locales.Text("表结构操作")).Grow(1).MinWidth(0).Padding(0).Gap(2)
		})
		ui.Row(c).Height(theme.RowHeight(c, theme.TabHeight)).Background(palette.Panel).Children(func() {
			names := []string{"字段", "索引", "外键", "表信息"}
			tabs := ui.TabsBase(c, &t.page, len(names))
			tabs.List.Gap(0).Children(func() {
				for i, name := range names {
					item := tabs.Tab(i).Height(theme.RowHeight(c, theme.TabHeight)).Padding(2, 9)
					if i == t.page {
						item.Background(palette.ToolbarSelected)
					}
					item.Children(func() { ui.Text(c, locales.Text(name)).FontSize(theme.Font(c, theme.FontSize)) })
				}
			})
		})
		if t.loading {
			ui.Row(c).Height(theme.RowHeight(c, theme.ControlHeight)).Gap(6).AlignItems(ui.Center).Padding(2, 6).Children(func() {
				ui.Spinner(c).Width(16).Height(16)
				ui.Text(c, locales.Text("正在读取真实 MySQL 表结构…")).TextColor(palette.Muted).FontSize(theme.Font(c, theme.FontSize)).SingleLine().Grow(1).MinWidth(0)
			})
		}
		if t.err != "" {
			ui.Row(c).Height(theme.RowHeight(c, theme.CommandBarHeight)).Padding(2, 6).AlignItems(ui.Center).Gap(6).Children(func() {
				ui.Text(c, locales.Text("加载结构失败：")+locales.Text(t.err)).FontSize(theme.Font(c, theme.FontSize)).TextColor(palette.Error).SingleLine().Grow(1).MinWidth(0).Tooltip(locales.Text(t.err))
				if ui.Button(c, locales.Text("重试读取结构")).Height(theme.RowHeight(c, theme.ControlHeight)).Disabled(!session.Connected || session.ProfileID != t.profileID || t.loading).Clicked() {
					s.Load(t, c.Services().Invalidate, true)
				}
			})
		}
		switch t.page {
		case 1:
			mysqlStructureIndexesView(c, t)
		case 2:
			mysqlStructureForeignKeysView(c, t)
		case 3:
			mysqlStructureInfoView(c, t.data)
		default:
			mysqlStructureFieldsView(c, t)
		}
	})
}

func mysqlStructureFieldsView(c *ui.Context, t *Document) {
	palette := theme.Colors(c)

	if t.data == nil || len(t.data.Columns) == 0 {
		message := "此表没有可见字段"
		if t.data == nil {
			message = "没有已加载的表结构"
		}
		theme.EmptyTable(c, &t.columns, mysqlStructureColumns, locales.Text(message))
		return
	}
	// Match the observed design grid: choosing a field shows its properties in
	// a lower pane. These are read-only values, never editing controls.
	t.columns.Selected = &t.fieldRow
	theme.Table(c, &t.columns, mysqlStructureColumns, len(t.data.Columns), func(row, col int) {
		f := t.data.Columns[row]
		var value string
		switch col {
		case 0:
			value = f.Name
		case 1:
			value = f.DataType
		case 2:
			value = mysqlStructureColumnLength(f)
		case 3:
			value = mysqlStructureColumnDecimals(f)
		case 4:
			if !f.Nullable {
				value = "✓"
			}
		case 5:
			if strings.Contains(strings.ToUpper(f.Extra), "VIRTUAL") {
				value = "✓"
			}
		case 6:
			value = f.Key
		case 7:
			value = mysqlStructureColumnDefault(f)
		case 8:
			value = f.Comment
		}
		ui.Text(c, value).FontSize(theme.Font(c, 12)).SingleLine()
	}).Grow(1)

	row := t.fieldRow
	if row < 0 || row >= len(t.data.Columns) {
		row = 0
	}
	field := t.data.Columns[row]
	yesNo := func(ok bool) string {
		if ok {
			return "是"
		}
		return "否"
	}
	_, height := c.Size()
	ui.ScrollBoth(c).Label(locales.Text("结构字段属性")).Height(min(float32(182), height*0.27)).Background(palette.Header).Children(func() {
		ui.Column(c).MinWidth(330).Padding(8, 10).Gap(7).Children(func() {
			ui.Row(c).Gap(16).AlignItems(ui.Center).Children(func() {
				ui.Text(c, locales.Text("字段属性：")+field.Name).FontSize(theme.Font(c, theme.FontSize)).Bold().Grow(1).MinWidth(0)
				ui.Box(c).Grow(1)
				ui.Text(c, "只读").FontSize(theme.Font(c, 11)).TextColor(palette.Muted)
			})
			ui.Row(c).Gap(16).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "默认值：").Width(75).TextColor(palette.Muted).FontSize(theme.Font(c, 12))
				ui.Text(c, mysqlStructureColumnDefault(field)).FontSize(theme.Font(c, theme.FontSize)).Grow(1).MinWidth(0)
			})
			ui.Row(c).Gap(20).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "自动递增："+yesNo(strings.Contains(strings.ToLower(field.Extra), "auto_increment"))).FontSize(theme.Font(c, 12))
				ui.Text(c, "无符号："+yesNo(strings.Contains(strings.ToLower(field.Type), "unsigned"))).FontSize(theme.Font(c, 12))
				ui.Text(c, "不是 null："+yesNo(!field.Nullable)).FontSize(theme.Font(c, 12))
			})
			ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "注释：").Width(75).TextColor(palette.Muted).FontSize(theme.Font(c, 12))
				ui.Text(c, field.Comment).FontSize(theme.Font(c, theme.FontSize)).Grow(1).MinWidth(0)
			})
		})
	})
}

func mysqlStructureIndexesView(c *ui.Context, t *Document) {
	if t.data == nil {
		theme.EmptyTable(c, &t.indexes, mysqlStructureIndexColumns, locales.Text("没有已加载的表结构"))
		return
	}
	indexes := mysqlStructureDesignIndexes(t.data.Indexes)
	if len(indexes) == 0 {
		theme.EmptyTable(c, &t.indexes, mysqlStructureIndexColumns, locales.Text("此表没有可见的次级索引"))
		return
	}
	theme.Table(c, &t.indexes, mysqlStructureIndexColumns, len(indexes), func(row, col int) {
		index := indexes[row]
		var value string
		switch col {
		case 0:
			value = index.Name
		case 1:
			value = mysqlStructureIndexColumnNames(index.Columns)
		case 2:
			if index.Unique {
				value = "UNIQUE"
			} else {
				value = "NORMAL"
			}
		case 3:
			value = index.Type
		}
		ui.Text(c, value).FontSize(theme.Font(c, 12)).SingleLine()
	}).Grow(1)
}

func mysqlStructureForeignKeysView(c *ui.Context, t *Document) {
	if t.data == nil || len(t.data.ForeignKeys) == 0 {
		message := "此表没有外键"
		if t.data == nil {
			message = "没有已加载的表结构"
		}
		theme.EmptyTable(c, &t.foreignKeys, mysqlStructureForeignColumns, locales.Text(message))
		return
	}
	theme.Table(c, &t.foreignKeys, mysqlStructureForeignColumns,
		len(t.data.ForeignKeys), func(row, col int) {
			fk := t.data.ForeignKeys[row]
			var value string
			switch col {
			case 0:
				value = fk.Name
			case 1:
				value = mysqlStructureFKColumnNames(fk.Columns, false)
			case 2:
				value = fk.ReferencedDatabase
			case 3:
				value = fk.ReferencedTable
			case 4:
				value = mysqlStructureFKColumnNames(fk.Columns, true)
			case 5:
				value = fk.OnDelete
			case 6:
				value = fk.OnUpdate
			}
			ui.Text(c, value).FontSize(theme.Font(c, 12)).SingleLine()
		}).Grow(1)
}

func mysqlStructureInfoView(c *ui.Context, structure *mysqlsvc.MySQLTableStructure) {
	palette := theme.Colors(c)

	if structure == nil {
		ui.Text(c, locales.Text("没有已加载的表结构")).Padding(12).TextColor(palette.Muted)
		ui.Box(c).Grow(1)
		return
	}
	rows := [][2]string{
		{"数据库", structure.Database}, {"表名称", structure.Name},
		{"对象类型", structure.Kind}, {"存储引擎", structure.Engine},
		{"排序规则", structure.Collation}, {"表注释", structure.Comment},
	}
	if structure.EstimatedRows != nil {
		rows = append(rows, [2]string{"估算行数", strconv.FormatInt(*structure.EstimatedRows, 10) + "（可能为近似值）"})
	}
	ui.Scroll(c).Grow(1).Children(func() {
		ui.Column(c).Padding(14).Gap(10).Children(func() {
			for _, r := range rows {
				row := r
				ui.Row(c).AlignItems(ui.Center).Gap(14).Children(func() {
					ui.Text(c, row[0]).Width(105).FontSize(theme.Font(c, 12)).TextColor(palette.Muted)
					ui.Text(c, row[1]).FontSize(theme.Font(c, theme.FontSize)).Grow(1).MinWidth(0)
				})
			}
		})
	})
}
