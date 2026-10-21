package shell

import (
	"fmt"
	"strconv"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

var mysqlStructureColumns = []ui.TableColumn{
	{Title: "#", Width: 54},
	{Title: "字段", Width: 170},
	{Title: "类型", Width: 190},
	{Title: "允许 NULL", Width: 95},
	{Title: "键", Width: 70},
	{Title: "默认值", Width: 140},
	{Title: "额外属性", Width: 165},
	{Title: "注释", Width: 220},
}
var mysqlStructureIndexColumns = []ui.TableColumn{
	{Title: "索引", Width: 190},
	{Title: "唯一", Width: 65},
	{Title: "类型", Width: 100},
	{Title: "顺序", Width: 62},
	{Title: "字段", Width: 180},
	{Title: "前缀长度", Width: 94},
	{Title: "方向", Width: 75},
}
var mysqlStructureForeignColumns = []ui.TableColumn{
	{Title: "外键", Width: 170},
	{Title: "字段", Width: 160},
	{Title: "引用数据库", Width: 140},
	{Title: "引用表", Width: 160},
	{Title: "引用字段", Width: 160},
	{Title: "更新规则", Width: 105},
	{Title: "删除规则", Width: 105},
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
				len(t.structure.Columns), len(t.structure.Indexes), len(t.structure.ForeignKeys))).
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
	ui.Table(c, &t.structureColumns, mysqlStructureColumns, len(t.structure.Columns), func(row, col int) {
		f := t.structure.Columns[row]
		var value string
		switch col {
		case 0:
			value = strconv.FormatInt(f.Position, 10)
		case 1:
			value = f.Name
		case 2:
			value = f.Type
		case 3:
			if f.Nullable {
				value = "是"
			} else {
				value = "否"
			}
		case 4:
			value = f.Key
		case 5:
			if f.Default == nil {
				value = "NULL"
			} else if *f.Default == "" {
				value = "\"\""
			} else {
				value = *f.Default
			}
		case 6:
			value = f.Extra
		case 7:
			value = f.Comment
		}
		ui.Text(c, value).FontSize(12).SingleLine()
	}).Grow(1)
}

type mysqlIndexViewRow struct {
	index connections.MySQLStructureIndex
	col   connections.MySQLStructureIndexColumn
}

func mysqlStructureIndexesView(c *ui.Context, t *tab) {
	var entries []mysqlIndexViewRow
	for _, index := range t.structure.Indexes {
		for _, column := range index.Columns {
			entries = append(entries, mysqlIndexViewRow{index, column})
		}
	}
	if len(entries) == 0 {
		ui.Text(c, "此表没有可见索引").Padding(14).TextColor(theme.Muted)
		return
	}
	ui.Table(c, &t.structureIndexes, mysqlStructureIndexColumns, len(entries), func(row, col int) {
		e := entries[row]
		var value string
		switch col {
		case 0:
			value = e.index.Name
		case 1:
			if e.index.Unique {
				value = "是"
			} else {
				value = "否"
			}
		case 2:
			value = e.index.Type
		case 3:
			value = strconv.FormatInt(e.col.Position, 10)
		case 4:
			value = e.col.Name
		case 5:
			if e.col.SubPart != nil {
				value = strconv.FormatInt(*e.col.SubPart, 10)
			}
		case 6:
			value = e.col.Direction
		}
		ui.Text(c, value).FontSize(12).SingleLine()
	}).Grow(1)
}

type mysqlForeignViewRow struct {
	key connections.MySQLStructureForeignKey
	col connections.MySQLStructureForeignKeyColumn
}

func mysqlStructureForeignKeysView(c *ui.Context, t *tab) {
	var entries []mysqlForeignViewRow
	for _, fk := range t.structure.ForeignKeys {
		for _, column := range fk.Columns {
			entries = append(entries, mysqlForeignViewRow{fk, column})
		}
	}
	if len(entries) == 0 {
		ui.Text(c, "此表没有外键").Padding(14).TextColor(theme.Muted)
		return
	}
	ui.Table(c, &t.structureForeignKeys, mysqlStructureForeignColumns, len(entries), func(row, col int) {
		e := entries[row]
		var value string
		switch col {
		case 0:
			value = e.key.Name
		case 1:
			value = e.col.Name
		case 2:
			value = e.key.ReferencedDatabase
		case 3:
			value = e.key.ReferencedTable
		case 4:
			value = e.col.ReferencedName
		case 5:
			value = e.key.OnUpdate
		case 6:
			value = e.key.OnDelete
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
