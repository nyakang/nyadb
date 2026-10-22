package shell

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

// The inspector follows the active real data tab rather than the connection
// profile, but never asks the server for an unbounded cell value.
func (s *State) mysqlDataInspector(c *ui.Context, t *tab) {
	ui.Column(c).Fill().Background(theme.Panel).Children(func() {
		ui.Row(c).Height(31).Padding(4, 10).AlignItems(ui.Center).Background(theme.Header).Children(func() {
			ui.Text(c, "字段信息").Bold().FontSize(12)
			ui.Box(c).Grow(1)
			if ui.Button(c, "×").Label("隐藏信息面板").Padding(2, 6).Clicked() {
				s.inspectorOn = false
			}
		})
		ui.Scroll(c).Grow(1).Children(func() {
			ui.Column(c).Padding(12).Gap(9).Children(func() {
				if t.dataPage == nil {
					message := "正在读取字段元数据…"
					if t.dataErr != "" {
						message = "字段元数据不可用，请重试读取数据"
					} else if !t.dataLoading {
						message = "尚未加载字段元数据"
					}
					ui.Text(c, message).TextColor(theme.Muted)
					return
				}
				field, found, complete := mysqlSelectedDataField(t)
				if !found {
					ui.Text(c, "字段不存在或已被隐藏").FontSize(12).TextColor(theme.Muted)
					return
				}
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Text(c, mysqlColumnTypeGlyph(field.DataType)).Bold().FontSize(17).TextColor(theme.Blue)
					ui.Text(c, field.Name).Bold().FontSize(15).SingleLine()
				})
				ui.Text(c, t.dataDatabase+"."+t.dataTable).FontSize(11).TextColor(theme.Muted).SingleLine()
				ui.Divider(c)
				mysqlInspectorProperty(c, "类型", field.Type)
				if !complete {
					ui.Text(c, "仅有基础字段类型，完整元数据尚未加载").FontSize(11).TextColor(theme.Muted)
				} else {
					mysqlInspectorFieldAttributes(c, field)
				}
				ui.Divider(c)
				name, cell, ok := mysqlSelectedDataCell(t)
				if !ok {
					ui.Text(c, "选择数据单元格以查看值的安全预览").FontSize(11).TextColor(theme.Muted)
					return
				}
				if t.dataChunkField != "" && (t.dataChunkField != name || t.dataChunkRow != t.dataSelectedRow) {
					s.cancelMySQLChunk(t)
				}
				mysqlInspectorProperty(c, "行号", fmt.Sprintf("%d", t.dataOffset+int64(t.dataSelectedRow)+1))
				if cell.Null {
					mysqlInspectorProperty(c, "值状态", "SQL NULL")
				} else if cell.Truncated {
					mysqlInspectorProperty(c, "值状态", "已截断的预览")
				} else {
					mysqlInspectorProperty(c, "值状态", "预览完整")
				}
				ui.Text(c, cell.Text).FontSize(12).SingleLine()
				if ui.Button(c, "复制单元格预览").Clicked() {
					c.WriteClipboard(mysqlDataCopyPreview(cell))
				}
				row := t.dataSelectedRow
				canRead := !cell.Null && row >= 0 && row < len(t.dataPage.RowRefs) &&
					t.dataPage.RowRefs[row] != nil
				if canRead {
					ui.Text(c, "大字段按需读取 · 单次最多 1024 字符/字节").FontSize(11).TextColor(theme.Muted)
					if ui.Button(c, "读取字段片段").Label("读取所选字段下一段").
						Disabled(t.dataChunkLoading || t.dataChunkDone).Clicked() {
						s.loadMySQLChunk(c.Services(), t)
					}
					if t.dataChunkLoading {
						ui.Text(c, "正在读取字段片段…").FontSize(11)
					}
					if t.dataChunkErr != "" {
						ui.Text(c, t.dataChunkErr).FontSize(11).TextColor(theme.Gold)
					}
					if t.dataChunkField == name && t.dataChunkRow == row && t.dataChunkNext > 0 {
						s.property(c, "已读位置", fmt.Sprintf("%d / %d", t.dataChunkNext, t.dataChunkTotal))
						ui.Text(c, t.dataChunkText).FontSize(11)
						if ui.Button(c, "复制此片段").Clicked() {
							c.WriteClipboard(t.dataChunkText)
						}
					}
				}
			})
		})
	})
}

// The reference inspector stacks property labels above values, so long MySQL
// declarations and comments can wrap without shrinking the data grid.
func mysqlInspectorProperty(c *ui.Context, label, value string) {
	ui.Column(c).Gap(2).Children(func() {
		ui.Text(c, label).FontSize(11).TextColor(theme.Muted)
		ui.Text(c, value).FontSize(12)
	})
}

func mysqlInspectorFieldAttributes(c *ui.Context, field connections.MySQLStructureColumn) {
	notNull := "否"
	if !field.Nullable {
		notNull = "是"
	}
	mysqlInspectorProperty(c, "不是 NULL", notNull)
	defaultValue := "SQL NULL / 未设置"
	if field.Default != nil {
		defaultValue = *field.Default
		// Make an actual string "NULL" or "" visibly distinct from SQL NULL.
		if mysqlInspectorTextType(field.DataType) {
			defaultValue = strconv.Quote(defaultValue)
		}
	}
	mysqlInspectorProperty(c, "默认值", defaultValue)
	key := "—"
	switch field.Key {
	case "PRI":
		key = "主键"
	case "UNI":
		key = "唯一索引"
	case "MUL":
		key = "索引（MUL）"
	}
	mysqlInspectorProperty(c, "键", key)
	autoIncrement := "否"
	if strings.Contains(strings.ToLower(field.Extra), "auto_increment") {
		autoIncrement = "是"
	}
	mysqlInspectorProperty(c, "自增", autoIncrement)
	if field.Extra != "" {
		mysqlInspectorProperty(c, "其他属性", field.Extra)
	}
	comment := field.Comment
	if comment == "" {
		comment = "—"
	}
	mysqlInspectorProperty(c, "注释", comment)
	if field.CharacterMaxLength != nil {
		mysqlInspectorProperty(c, "字符长度", strconv.FormatInt(*field.CharacterMaxLength, 10))
	}
	if field.NumericPrecision != nil {
		mysqlInspectorProperty(c, "数值精度", strconv.FormatInt(*field.NumericPrecision, 10))
	}
	if field.NumericScale != nil {
		mysqlInspectorProperty(c, "小数位", strconv.FormatInt(*field.NumericScale, 10))
	}
	if field.DatetimePrecision != nil {
		mysqlInspectorProperty(c, "时间精度", strconv.FormatInt(*field.DatetimePrecision, 10))
	}
	if field.CharacterSet != nil {
		mysqlInspectorProperty(c, "字符集", *field.CharacterSet)
	}
	if field.Collation != nil {
		mysqlInspectorProperty(c, "排序规则", *field.Collation)
	}
}

func mysqlInspectorTextType(kind string) bool {
	return strings.Contains(" char varchar tinytext text mediumtext longtext enum set json ", " "+strings.ToLower(kind)+" ")
}
