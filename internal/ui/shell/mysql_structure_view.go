package shell

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
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
	{Title: "名称", Width: 160},
	{Title: "字段", Width: 175},
	{Title: "被引用的数据库", Width: 140},
	{Title: "被引用的表", Width: 145},
	{Title: "被引用的字段", Width: 185},
	{Title: "删除时", Width: 95},
	{Title: "更新时", Width: 95},
}

func (s *State) mysqlStructureWorkspace(c *ui.Context, t *tab) {
	ui.Column(c).Fill().Background(theme.App).Children(func() {
		ui.Row(c).Height(39).Padding(5, 10).AlignItems(ui.Center).Gap(10).
			Background(theme.Header).Children(func() {
			theme.Icon(c, "table", 16, theme.Blue)
			ui.Text(c, t.structureDatabase+" / "+t.structureTable).Bold().FontSize(13).SingleLine()
			ui.Text(c, "只读结构").TextColor(theme.Muted).FontSize(11)
			ui.Box(c).Grow(1)
			if ui.Button(c, "返回对象列表").Clicked() {
				s.activeTab = 0
			}
			if ui.Button(c, "刷新结构").Label("刷新表结构：" + t.structureTable).
				Disabled(s.mysqlSessionStatus != "已连接" || t.structureLoading).Clicked() {
				s.loadMySQLStructure(c.Services(), t, true)
			}
			if t.structureLoading && ui.Button(c, "取消加载").Clicked() {
				s.cancelMySQLStructure(t)
				t.structureErr = "加载已取消"
			}
		})
		if t.structureLoading {
			ui.Row(c).Height(32).Gap(8).AlignItems(ui.Center).Padding(6, 12).Children(func() {
				ui.Spinner(c).Width(16).Height(16)
				ui.Text(c, "正在读取真实 MySQL 表结构…").TextColor(theme.Muted).FontSize(12)
			})
		}
		if t.structureErr != "" {
			ui.Row(c).Padding(8, 12).AlignItems(ui.Center).Gap(12).Children(func() {
				ui.Text(c, "加载结构失败："+t.structureErr).FontSize(12).TextColor(theme.Gold)
				if ui.Button(c, "重试读取结构").Disabled(s.mysqlSessionStatus != "已连接" || t.structureLoading).Clicked() {
					s.loadMySQLStructure(c.Services(), t, true)
				}
			})
		}
		if t.structure == nil {
			if !t.structureLoading && t.structureErr == "" {
				ui.Text(c, "没有已加载的表结构").Padding(12).TextColor(theme.Muted)
			}
			ui.Box(c).Grow(1)
			return
		}
		ui.Row(c).Height(29).AlignItems(ui.Center).Padding(4, 12).Gap(14).Children(func() {
			ui.Text(c, "类型："+t.structure.Kind).FontSize(11).TextColor(theme.Muted)
			ui.Text(c, "引擎："+t.structure.Engine).FontSize(11).TextColor(theme.Muted)
			ui.Text(c, fmt.Sprintf("字段 %d · 索引 %d · 外键 %d",
				len(t.structure.Columns), len(mysqlStructureDesignIndexes(t.structure.Indexes)), len(t.structure.ForeignKeys))).
				FontSize(11).TextColor(theme.Muted)
		})
		ui.Tabs(c, &t.structurePage, "字段", "索引", "外键", "表信息").Padding(4, 9)
		switch t.structurePage {
		case 1:
			mysqlStructureIndexesView(c, t)
		case 2:
			mysqlStructureForeignKeysView(c, t)
		case 3:
			mysqlStructureInfoView(c, t.structure)
		default:
			mysqlStructureFieldsView(c, t)
		}
	})
}

func mysqlStructureFieldsView(c *ui.Context, t *tab) {
	if len(t.structure.Columns) == 0 {
		ui.Text(c, "此表没有可见字段").Padding(14).TextColor(theme.Muted)
		return
	}
	// Match the observed design grid: choosing a field shows its properties in
	// a lower pane. These are read-only values, never editing controls.
	t.structureColumns.Selected = &t.structureFieldRow
	ui.Table(c, &t.structureColumns, mysqlStructureColumns, len(t.structure.Columns), func(row, col int) {
		f := t.structure.Columns[row]
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
		ui.Text(c, value).FontSize(12).SingleLine()
	}).Grow(1)

	row := t.structureFieldRow
	if row < 0 || row >= len(t.structure.Columns) {
		row = 0
	}
	field := t.structure.Columns[row]
	yesNo := func(ok bool) string {
		if ok {
			return "是"
		}
		return "否"
	}
	ui.Column(c).Height(141).Background(theme.Header).Padding(8, 12).Gap(7).Children(func() {
		ui.Row(c).Gap(16).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "字段属性："+field.Name).FontSize(12).Bold()
			ui.Box(c).Grow(1)
			ui.Text(c, "只读").FontSize(11).TextColor(theme.Muted)
		})
		ui.Row(c).Gap(16).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "默认值：").Width(75).TextColor(theme.Muted).FontSize(12)
			ui.Text(c, mysqlStructureColumnDefault(field)).FontSize(12).SingleLine()
		})
		ui.Row(c).Gap(20).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "自动递增："+yesNo(strings.Contains(strings.ToLower(field.Extra), "auto_increment"))).FontSize(12)
			ui.Text(c, "无符号："+yesNo(strings.Contains(strings.ToLower(field.Type), "unsigned"))).FontSize(12)
			ui.Text(c, "不是 null："+yesNo(!field.Nullable)).FontSize(12)
		})
		ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "注释：").Width(75).TextColor(theme.Muted).FontSize(12)
			ui.Text(c, field.Comment).FontSize(12).SingleLine()
		})
	})
}

func mysqlStructureIndexesView(c *ui.Context, t *tab) {
	indexes := mysqlStructureDesignIndexes(t.structure.Indexes)
	if len(indexes) == 0 {
		ui.Text(c, "此表没有可见的次级索引").Padding(14).TextColor(theme.Muted)
		return
	}
	ui.Table(c, &t.structureIndexes, mysqlStructureIndexColumns, len(indexes), func(row, col int) {
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
		ui.Text(c, value).FontSize(12).SingleLine()
	}).Grow(1)
}

func mysqlStructureForeignKeysView(c *ui.Context, t *tab) {
	if len(t.structure.ForeignKeys) == 0 {
		ui.Text(c, "此表没有外键").Padding(14).TextColor(theme.Muted)
		return
	}
	ui.Table(c, &t.structureForeignKeys, mysqlStructureForeignColumns,
		len(t.structure.ForeignKeys), func(row, col int) {
			fk := t.structure.ForeignKeys[row]
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
			ui.Text(c, value).FontSize(12).SingleLine()
		}).Grow(1)
}

func mysqlStructureInfoView(c *ui.Context, structure *connections.MySQLTableStructure) {
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
					ui.Text(c, row[0]).Width(105).FontSize(12).TextColor(theme.Muted)
					ui.Text(c, row[1]).FontSize(12)
				})
			}
		})
	})
}
